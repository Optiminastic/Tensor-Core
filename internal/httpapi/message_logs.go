package httpapi

// Every WhatsApp message the win-back agent has sent, and the ones it could not.
//
// READ FROM TENSOR'S OWN LEDGER, not from Meta. The voice half joins Sarvam's
// attempt list because Sarvam is the system of record for what happened on a
// call. Meta is not the equivalent for messages: without the delivery webhook
// there is no per-message status to fetch at all, so the ledger row written at
// send time is the whole truth and fetching anything would only add latency.
//
// WHICH IS WHY THE STATUS IS NARROW, deliberately. "Sent" here means Meta
// accepted the message, not that a phone displayed it - Meta will accept a
// send to a number that is not on WhatsApp, and India's per-recipient
// marketing cap drops accepted messages silently. Measured on this account: a
// utility template arrived while a marketing one, sent in the same minute from
// the same number, did not. Calling that "Delivered" would be a lie the data
// cannot support.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// defaultMessageLogRows bounds the listing.
const defaultMessageLogRows = 200

func (s *Server) registerMessageLogs(r *gin.Engine) {
	g := r.Group("/brands/:slug/message-logs")
	g.Use(s.guards.RequireUser())
	// order:read, like the call logs beside it: the same customers, the same
	// baskets, reached a different way.
	g.GET("", s.guards.RequirePermission(auth.OrderRead.Key()), s.listMessageLogs)
}

type messageLogResponse struct {
	// The basket this message was about.
	CheckoutID   string `json:"checkout_id"`
	CheckoutName string `json:"checkout_name"`
	CustomerName string `json:"customer_name"`
	Phone        string `json:"phone"`

	// Status is one of three words and no more: "sent", "failed" or
	// "pending". Narrow on purpose - see the note at the top of this file.
	Status string `json:"status"`
	// Detail is why, in the sender's own words. Meta's sentence on a refusal,
	// or Tensor's when it would not build a link.
	Detail string `json:"detail"`
	// MessageID is Meta's wamid, empty when nothing was sent. It is the key a
	// delivery webhook would correlate on, the day there is one.
	MessageID string `json:"message_id"`
	Attempts  int    `json:"attempts"`
	SentAt    string `json:"sent_at"`
	CreatedAt string `json:"created_at"`
}

type messageLogsResponse struct {
	Items []messageLogResponse `json:"items"`
}

func (s *Server) listMessageLogs(c *gin.Context) {
	ctx := c.Request.Context()

	rows := defaultMessageLogRows
	if raw := c.Query("rows"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 1000 {
			detail(c, http.StatusUnprocessableEntity,
				"rows must be a whole number between 1 and 1000.")
			return
		}
		rows = parsed
	}

	channel := channelWhatsApp
	records, err := s.store.Q.ListCheckoutCalls(ctx, gen.ListCheckoutCallsParams{
		BrandSlug: c.Param("slug"), Channel: &channel, RowLimit: int32(rows),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the message log.")
		return
	}

	items := make([]messageLogResponse, 0, len(records))
	for _, r := range records {
		items = append(items, toMessageLog(r))
	}
	c.JSON(http.StatusOK, messageLogsResponse{Items: items})
}

func toMessageLog(r gen.AbandonedCheckoutCall) messageLogResponse {
	out := messageLogResponse{
		CheckoutID:   r.CheckoutID,
		CheckoutName: r.CheckoutName,
		CustomerName: r.CustomerName,
		Phone:        r.Phone,
		Status:       messageStatus(r.Status),
		Attempts:     int(r.Attempts),
	}
	if r.AttemptID != nil {
		out.MessageID = *r.AttemptID
	}
	if r.Detail != nil {
		out.Detail = *r.Detail
	}
	if r.LastAttemptAt.Valid {
		out.SentAt = r.LastAttemptAt.Time.UTC().Format(time.RFC3339)
	}
	if r.CreatedAt.Valid {
		out.CreatedAt = r.CreatedAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}

// messageStatus collapses the ledger's vocabulary into the three states a
// reader can act on.
//
// 'claimed' becomes "pending", and it is not a success. A claim is written
// before the send and settled after, so a row still reading 'claimed' means
// the process died in between - the message may or may not have gone. Showing
// it as sent would hide a crash; showing it as failed would invent one.
func messageStatus(stored string) string {
	switch stored {
	case "sent":
		return "sent"
	case "failed":
		return "failed"
	default:
		return "pending"
	}
}
