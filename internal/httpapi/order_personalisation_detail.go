package httpapi

// What this order's customer was asked, and what they changed.
//
// One request behind the Personalisation tab on an order, because the two
// halves answer the same question and are read together: "we messaged them -
// what did it say, and did they correct anything?" Splitting them would make
// the tab wait on two round trips to show one story.
//
// THE MESSAGE IS REBUILT, NOT STORED, and unlike the win-back log's
// reconstruction that is exact rather than a best effort. The copy lives at
// Meta and is fetched and cached; the three values are computed by
// personalisationBodyParams from the ORDER, which is the same function the
// sender called on the same row. The only way this can drift is if the order
// itself were edited after the message went out, and nothing in Tensor edits
// an imported order.
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
	// Reconstructed is always true and says so plainly. The words are Meta's
	// and the values are the order's; nothing was stored at send time.
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
		out.Message = s.rebuildPersonalisationMessage(ctx, gen.Order(order), link)
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

// rebuildPersonalisationMessage fills the approved copy with this order's
// values. Returns nil when the template cannot be read, rather than quoting a
// message with {{1}} still in it.
func (s *Server) rebuildPersonalisationMessage(
	ctx context.Context, order gen.Order, link gen.PersonalisationLink,
) *personalisationMessage {
	msg := &personalisationMessage{
		To: link.Phone, Status: link.Status, Reconstructed: true,
		Template: strings.TrimSpace(s.cfg.PersonalisationTemplate),
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
