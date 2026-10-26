package httpapi

// Resolving the bot's one-off messages: the shop's wording if it has written
// one, the binary's if it has not.
//
// READ EVERY TIME, and that is the point rather than an oversight. The Bot
// Messages page promises "changes apply to the next message", and a cache is
// exactly how that promise gets broken: somebody fixes a typo in the Rs 100
// ask, watches it save, and the bot keeps sending the old words until an API
// restart nobody connects to the edit. The table holds a handful of rows
// behind its primary key, so the read costs less than the message it is
// about to send.
//
// NEVER FAILS. A database that will not answer must not stop a customer being
// told their pickup is booked, so every error here falls through to the
// wording in the binary - which is always right, just not rewritten.

import (
	"context"
	"fmt"

	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// notices is one brand's rewritten messages, by key.
type notices map[string]string

// noticesFor reads what this brand has rewritten. An empty map is the normal
// case and means "send what the binary says".
func (s *Server) noticesFor(ctx context.Context, brand string) notices {
	rows, err := s.store.Q.ListBotMessages(ctx, brand)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the bot's rewritten messages; "+
			"sending the shipped wording", "brand", brand, "error", err)
		return nil
	}
	out := make(notices, len(rows))
	for _, r := range rows {
		out[r.MessageKey] = r.Body
	}
	return out
}

// text is the wording to send for one key.
//
// THE DEFAULT COMES FROM THE REGISTRY, not from the caller, so a key that is
// mistyped cannot silently send an empty message - it sends nothing and says
// so, which is a bug somebody notices rather than a customer receiving a
// blank WhatsApp.
func (n notices) text(key string) string {
	notice, known := content.NoticeByKey(key)
	if !known {
		// A programming error, not a configuration one. Returning "" here
		// would have the bot send an empty message; the key is returned
		// instead so it shows up the first time anybody looks.
		return "[" + key + "]"
	}
	if body, ok := n[key]; ok && body != "" {
		return body
	}
	return notice.Default
}

// format is text with the order number substituted in.
//
// Separate from text because only three messages take one, and because a
// rewrite that lost its %s would otherwise send "%!s(MISSING)" to a customer.
// The API refuses such a save, and this is the second line of defence for a
// row written before that check existed.
func (n notices) format(key string, args ...any) string {
	body := n.text(key)
	notice, known := content.NoticeByKey(key)
	if known && content.CountPlaceholders(body) != notice.Placeholders {
		body = notice.Default
	}
	return fmt.Sprintf(body, args...)
}

// noticeText is the one-shot form, for a caller that needs a single message
// and has no reason to hold the whole set.
func (s *Server) noticeText(ctx context.Context, brand, key string) string {
	return s.noticesFor(ctx, brand).text(key)
}

// Message keys, as constants rather than strings at the call sites.
//
// A mistyped key is caught by the compiler here rather than by a customer
// receiving "[revrse_pickup_booked]".
const (
	noticeDamageLinkBody      = "damage_link_body"
	noticeDamageLinkButton    = "damage_link_button"
	noticeDamageLinkAlready   = "damage_link_already"
	noticeDamageLinkFailed    = "damage_link_failed"
	noticeReprintScheduled    = "reprint_scheduled"
	noticeReversePaymentAsk   = "reverse_payment_request"
	noticeReversePaymentNoQR  = "reverse_payment_request_no_qr"
	noticeReversePickupBooked = "reverse_pickup_booked"
)

// storedNotices is what the Bot Messages page reads: every editable message,
// with the shop's wording where there is one.
func (s *Server) storedNotices(ctx context.Context, brand string) []noticeJSON {
	stored := s.noticesFor(ctx, brand)
	out := make([]noticeJSON, 0, len(content.Notices()))
	for _, n := range content.Notices() {
		body, edited := stored[n.Key]
		if !edited || body == "" {
			body, edited = n.Default, false
		}
		out = append(out, noticeJSON{
			Key: n.Key, Label: n.Label, Group: n.Group, Help: n.Help,
			Body: body, Default: n.Default,
			Edited: edited, Placeholders: n.Placeholders, Limit: n.Limit,
		})
	}
	return out
}

// noticeJSON is one message as the page sees it.
//
// IT CARRIES ITS DEFAULT. The page offers "put this back", and doing that
// without a round trip - and without the frontend keeping its own copy of
// wording that lives in the backend - needs the original beside the current.
type noticeJSON struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"`
	Help  string `json:"help"`
	// Body is what is sent today: the shop's wording, or the default.
	Body string `json:"body"`
	// Default is the wording in the binary, for the reset button.
	Default string `json:"default"`
	// Edited says which of the two Body is, so the page can mark it.
	Edited bool `json:"edited"`
	// Placeholders is how many %s this message must contain.
	Placeholders int `json:"placeholders"`
	Limit        int `json:"limit"`
}
