package httpapi

// What this order's customer was asked, and what they changed.
//
// One request behind the Personalisation tab on an order, because the two
// halves answer the same question and are read together: "we messaged them -
// what did it say, and did they correct anything?" Splitting them would make
// the tab wait on two round trips to show one story.
//
// THE MESSAGE IS THE ONE THAT WAS SENT, read back from the row the sender
// froze it on (migration 0104). It used to be rebuilt on every read from the
// template THIS process is configured with, which is exact only while every
// process agrees on the name - and they did not. tensor-api carries no
// PERSONALISATION_TEMPLATE_NAME, the rebuild therefore asked for "", and
// whatsapp.Client.Template falls back to the configured default: this tab
// quoted the brand's CART-RECOVERY message to an operator asking what a
// customer had been told about their order.
//
// Rows written before that migration are still rebuilt, because there is
// nothing else to show them - but only ever by explicit name, so the fallback
// that caused this cannot happen again. A blank name now means no body at
// all, which is honest, rather than somebody else's words.
//
// THE TOKEN IS NOT HERE. The body is the words, and the buttons are named
// rather than linked. A live link is a credential and has its own route, which
// hands it over only while the window is open.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
)

type orderPersonalisationResponse struct {
	// SkipReason is why nobody was asked, when nobody was. The rule the
	// sender named, kept on the order - see migration 0105. Absent when a
	// message did go out, and absent on an order the sender has not reached a
	// verdict on yet.
	SkipReason *string `json:"skip_reason,omitempty"`
	// Message is absent when this order was never asked - the feature was off,
	// the order predates the watermark, there was no phone. The tab says so
	// rather than showing an empty quotation.
	Message *personalisationMessage `json:"message"`
	// Items is every job this order made that a customer could change, with
	// what they changed. Present even when no message was sent: somebody in
	// the shop may have corrected it by hand.
	Items []personalisationItemChanges `json:"items"`
}

type personalisationMessage struct {
	Template string `json:"template"`
	Language string `json:"language"`
	// Body is the approved copy with this order's own values in it, exactly as
	// the customer read it - asterisks and emoji included, since that is what
	// WhatsApp rendered.
	Body string `json:"body"`
	// Buttons is how many buttons sat under the message. Not their labels:
	// the Template the API hands back carries each URL button's url, and the
	// label is not among them. A count is honest; an invented label is not.
	Buttons int    `json:"buttons"`
	To      string `json:"to"`
	Status  string `json:"status"`
	// Detail is the sender's own note - "sent to the test recipient, not the
	// customer" for a redirected send, or Meta's refusal.
	Detail    string  `json:"detail,omitempty"`
	SentAt    *string `json:"sent_at,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	// Reconstructed is true only for a row sent before the copy was frozen,
	// whose body is today's template filled with this order's values rather
	// than the words that actually left. The tab says which it is looking at,
	// because they are not the same claim.
	Reconstructed bool `json:"reconstructed"`
}

type personalisationItemChanges struct {
	JobID       string                  `json:"job_id"`
	JobNumber   string                  `json:"job_number"`
	ProductName string                  `json:"product_name"`
	Changes     []personalisationChange `json:"changes"`
}

// orderPersonalisation answers the Personalisation tab.
func (s *Server) orderPersonalisation(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	order, err := s.store.Q.GetOrderByID(ctx, id)
	if err != nil {
		dbError(c, err, "That order does not exist.", "Could not load the order.")
		return
	}

	out := orderPersonalisationResponse{Items: s.personalisationChangesForOrder(ctx, id)}
	if link, err := s.store.Q.GetPersonalisationLinkRowForOrder(ctx, id); err == nil {
		out.Message = s.personalisationMessageFor(ctx, gen.Order(order), link)
	} else if order.PersonalisationSkipReason != nil {
		// No link, but the sender did reach a verdict. That verdict is the
		// whole answer to "why does this order show nothing", and it is the
		// reason somebody opened this tab.
		out.SkipReason = order.PersonalisationSkipReason
	}
	c.JSON(http.StatusOK, out)
}

// personalisationChangesForOrder is what the customer corrected, per item.
func (s *Server) personalisationChangesForOrder(
	ctx context.Context, orderID uuid.UUID,
) []personalisationItemChanges {
	jobs, err := s.store.Q.ListProductionJobsForOrder(ctx, &orderID)
	if err != nil {
		return nil
	}
	out := make([]personalisationItemChanges, 0, len(jobs))
	for _, j := range jobs {
		job := gen.ProductionJob(j)
		changes := personalisationChangesFor(job)
		// Charge lines and anything else nobody makes are dropped: this tab is
		// about what the customer typed, and a fee has nothing to type.
		if isChargeLine(deref(job.ProductName)) {
			continue
		}
		out = append(out, personalisationItemChanges{
			JobID: job.ID.String(), JobNumber: job.JobNumber,
			ProductName: deref(job.ProductName), Changes: changes,
		})
	}
	return out
}

// personalisationMessageFor is what this customer was sent.
//
// The frozen copy when there is one, and only then today's template as a
// fallback for a row that predates the freezing.
func (s *Server) personalisationMessageFor(
	ctx context.Context, order gen.Order, link gen.PersonalisationLink,
) *personalisationMessage {
	msg := &personalisationMessage{
		To: link.Phone, Status: link.Status,
		Template: strings.TrimSpace(deref(link.TemplateName)),
	}
	if link.Detail != nil {
		msg.Detail = *link.Detail
	}
	if link.SentAt.Valid {
		at := link.SentAt.Time.UTC().Format(time.RFC3339)
		msg.SentAt = &at
	}
	if link.ExpiresAt.Valid {
		at := link.ExpiresAt.Time.UTC().Format(time.RFC3339)
		msg.ExpiresAt = &at
	}

	// The words the sender wrote down, which need nothing from Meta and
	// cannot be the wrong template.
	if body := strings.TrimSpace(deref(link.MessageBody)); body != "" {
		msg.Body = body
		if link.ButtonCount != nil {
			msg.Buttons = int(*link.ButtonCount)
		}
		return msg
	}

	// Older row: rebuild it, and say so.
	msg.Reconstructed = true
	if msg.Template == "" {
		// NO FALLBACK. An empty name sent to the client means "whatever this
		// process's default template is", which is the cart-recovery one -
		// and quoting that here is worse than quoting nothing, because it
		// reads as a message this customer was sent.
		msg.Template = strings.TrimSpace(s.cfg.PersonalisationTemplate)
	}
	if msg.Template == "" {
		return msg
	}
	messenger := s.whatsappFor(ctx, link.BrandSlug)
	if messenger == nil || !messenger.Configured() {
		return msg
	}
	tpl, err := messenger.Template(ctx, msg.Template, "")
	if err != nil {
		// The words are at Meta and Meta is not answering. The row is still
		// worth showing - who, when, what came back - so the message is
		// returned without its body rather than withheld entirely.
		return msg
	}
	msg.Language = tpl.Language
	msg.Body = whatsapp.Fill(tpl.Body, personalisationBodyParams(order))
	msg.Buttons = len(tpl.ButtonURLs)
	return msg
}
