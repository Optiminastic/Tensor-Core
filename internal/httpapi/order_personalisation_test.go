package httpapi

// The Personalisation column, and the one thing it must never get wrong.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func at(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// ListPersonalisationLinkStatesForOrders became a LEFT JOIN from orders, so
// an order nobody messaged now arrives here with an empty status instead of
// not arriving at all. Falling through to the default would draw "Done" on
// it - a row claiming this customer confirmed values nobody ever showed them,
// which is how an order gets printed on unchecked spelling.
func TestAnOrderWithNoLinkIsNotDone(t *testing.T) {
	got := windowStateOf(gen.ListPersonalisationLinkStatesForOrdersRow{Status: ""})
	if got != "" {
		t.Fatalf("an order with no link read as %q, want no state at all", got)
	}
}

func TestWindowStates(t *testing.T) {
	soon := at(time.Now().Add(10 * time.Minute))
	past := at(time.Now().Add(-10 * time.Minute))

	cases := []struct {
		name string
		row  gen.ListPersonalisationLinkStatesForOrdersRow
		want string
	}{
		{"sent and live", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "sent", ExpiresAt: soon}, personalisationWindowOpen},
		{"opened and live", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "opened", ExpiresAt: soon}, personalisationWindowOpen},
		// Past its instant but not yet swept: the customer cannot use it.
		{"sent and past", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "sent", ExpiresAt: past}, personalisationWindowDone},
		{"confirmed", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "confirmed"}, personalisationWindowDone},
		{"expired", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "expired"}, personalisationWindowDone},
		// Meta refused it. Nobody was asked, and the row must not imply they were.
		{"failed", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "failed"}, personalisationWindowNotSent},
		// Claimed, not sent. There is no link in anybody's hand yet.
		{"pending", gen.ListPersonalisationLinkStatesForOrdersRow{
			Status: "pending"}, ""},
	}
	for _, tc := range cases {
		if got := windowStateOf(tc.row); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The bug this tab shipped with: the message was rebuilt on read from the
// template THIS process is configured with, and whatsapp.Client.Template
// falls back to its configured default when handed a blank name. tensor-api
// carries no PERSONALISATION_TEMPLATE_NAME, so the tab quoted the brand's
// CART-RECOVERY template to an operator asking what a customer had been told
// about their order. Seen in production on 15 Oct 2026.
//
// Nothing is better than somebody else's words: a quoted message is read as
// what this customer received.
func TestABlankTemplateNameQuotesNothing(t *testing.T) {
	s := &Server{}
	msg := s.personalisationMessageFor(t.Context(), gen.Order{},
		gen.PersonalisationLink{Phone: "919000000001", Status: "sent"})

	if msg == nil {
		t.Fatal("the row should still be shown - who, when, what came back")
	}
	if msg.Body != "" {
		t.Errorf("quoted %q with no template to quote from", msg.Body)
	}
	if msg.Template != "" {
		t.Errorf("named template %q, which this send did not use", msg.Template)
	}
}

// A row whose copy was frozen at send time needs nothing from Meta, and what
// it shows is the message itself rather than a reconstruction of it.
func TestTheFrozenCopyIsQuotedAsSent(t *testing.T) {
	body := "Hi Ananya! Thank you for your order. 😊"
	name := "order_personalisation_check"
	s := &Server{}
	msg := s.personalisationMessageFor(t.Context(), gen.Order{},
		gen.PersonalisationLink{
			Phone: "919000000001", Status: "sent",
			TemplateName: &name, MessageBody: &body,
		})

	if msg.Body != body {
		t.Errorf("quoted %q, want the frozen body %q", msg.Body, body)
	}
	if msg.Template != name {
		t.Errorf("named %q, want the template this send actually used", msg.Template)
	}
	if msg.Reconstructed {
		t.Error("a frozen body is the message, not a reconstruction of it")
	}
}
