package httpapi

// Who gets asked to check their order, and - far more importantly - who does
// not. A bug in these rules is not a wrong line on a page; it is a stranger
// being messaged about somebody else's order, or a month of delivered orders
// being messaged about at once.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func orderWith(items ...production.LineItem) gen.Order {
	raw, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	return gen.Order{OrderNumber: "#66897853350101", LineItems: raw}
}

func personalised(qty int) production.LineItem {
	return production.LineItem{
		ProductName: "Dual Name Plank", Quantity: qty,
		PersonalisationRequired: true,
	}
}

func plain(qty int) production.LineItem {
	return production.LineItem{ProductName: "Spool holder", Quantity: qty}
}

func TestAnOrderOfPlainStockIsLeftAlone(t *testing.T) {
	// Nothing on it was made to anybody's instructions, so there is nothing to
	// confirm - and the one message this customer will read should not be
	// spent asking them to agree with themselves.
	if orderNeedsPersonalisation(orderWith(plain(2))) {
		t.Error("a plain order was treated as personalised")
	}
	if !orderNeedsPersonalisation(orderWith(plain(1), personalised(1))) {
		t.Error("an order with one personalised line was treated as plain")
	}
}

func TestAnUnreadableOrderIsLeftAlone(t *testing.T) {
	// The safe answer to "I cannot read this order" is to message nobody.
	broken := gen.Order{OrderNumber: "#1", LineItems: []byte("{not json")}
	if orderNeedsPersonalisation(broken) {
		t.Error("an undecodable order was treated as personalised")
	}
}

func TestTheAllowListIsReadTheWayAnOrderNumberIsPrinted(t *testing.T) {
	// It is typed into an environment variable by somebody reading the number
	// off a screen, where it shows as "#66897853350101". A gate that silently
	// matches nothing because of a hash looks armed and is not.
	for _, typed := range []string{
		"#66897853350101", "66897853350101", " #66897853350101 ", "#66897853350101\n",
	} {
		if !matchesOrderNumber([]string{typed}, "#66897853350101") {
			t.Errorf("allow-list entry %q did not match the order", typed)
		}
	}
	if matchesOrderNumber([]string{"#66897853350102"}, "#66897853350101") {
		t.Error("a different order number matched")
	}
	if matchesOrderNumber([]string{""}, "#66897853350101") {
		t.Error("an empty allow-list entry matched")
	}
}

func TestAnOrderWithNoNumberMatchesNothing(t *testing.T) {
	// Otherwise an unnumbered order would match an empty allow-list entry and
	// slip past the testing gate.
	if matchesOrderNumber([]string{""}, "") {
		t.Error("an unnumbered order matched the allow-list")
	}
}

func TestTheWatermarkIsComparedAgainstWhenTheCustomerPaid(t *testing.T) {
	// Orders are PULLED, never pushed, so imported_at is when a sync happened
	// to run and placed_at is when the customer actually bought. Comparing the
	// wrong one would message somebody whose order was placed last week and
	// imported this morning.
	placed := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	imported := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

	order := gen.Order{
		PlacedAt:   db.Timestamptz(&placed),
		ImportedAt: db.Timestamptz(&imported),
	}
	if got := orderPlacedAt(order); !got.Equal(placed) {
		t.Errorf("placed at %v, want the customer's own moment %v", got, placed)
	}

	// Older orders predate the placed_at column, so the import time is all
	// there is.
	noPlaced := gen.Order{ImportedAt: db.Timestamptz(&imported)}
	if got := orderPlacedAt(noPlaced); !got.Equal(imported) {
		t.Errorf("fallback = %v, want %v", got, imported)
	}

	if got := orderPlacedAt(gen.Order{}); !got.IsZero() {
		t.Errorf("an order with neither timestamp = %v, want zero", got)
	}
}

func TestTheMessageCountsUnitsNotLines(t *testing.T) {
	// Three of the same plank is three things to check. "1 item" about three
	// planks reads as though the shop has lost two of them.
	order := orderWith(personalised(3))
	if got := personalisedItemCount(order); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
	// Plain lines are not things the customer can change, so they are not
	// counted.
	mixed := orderWith(personalised(2), plain(5))
	if got := personalisedItemCount(mixed); got != 2 {
		t.Errorf("count = %d, want 2 - the plain line is not checkable", got)
	}
}

func TestEveryPersonalisationParameterIsNonEmpty(t *testing.T) {
	// Meta refuses an empty parameter and refuses the WHOLE message with it.
	// A missing customer name is ordinary: a Shopify order can reach payment
	// with an email and nothing else.
	bare := gen.Order{LineItems: []byte("[]")}
	for i, p := range personalisationBodyParams(bare) {
		if p == "" {
			t.Errorf("parameter %d is empty, which Meta refuses", i+1)
		}
	}

	named := orderWith(personalised(2))
	named.CustomerName = strPtr("Rishi Patel")
	got := personalisationBodyParams(named)
	want := []string{"Rishi Patel", "#66897853350101", "2 personalised items"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parameter %d = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestTheItemCountReadsAsASentence(t *testing.T) {
	// The template emphasises this parameter and adds no noun of its own, so
	// the plural has to be right here. "your 1 personalised items" is what a
	// count on its own produces for the commonest order there is.
	for n, want := range map[int]string{
		0: "1 personalised item", // never said at all, but never said as "0"
		1: "1 personalised item",
		2: "2 personalised items",
	} {
		if got := spokenItemCount(n); got != want {
			t.Errorf("spokenItemCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestACustomerNameWithANewlineIsFlattened(t *testing.T) {
	// Meta refuses a parameter containing a newline, a tab, or four or more
	// consecutive spaces - and a shopper can absolutely type one.
	order := orderWith(personalised(1))
	order.CustomerName = strPtr("Rishi\nPatel")
	if got := personalisationBodyParams(order)[0]; got != "Rishi Patel" {
		t.Errorf("name = %q, want the newline flattened", got)
	}
}

func TestATestRecipientTakesEveryMessage(t *testing.T) {
	// Not a filter that lets some customers through - a redirect that takes
	// all of them. Testing against a live shop means testing against real
	// people's orders, and the only safe way is for every message to land on
	// one phone belonging to whoever is testing.
	to, redirected := personalisationRecipient("9799931864", "9136361465")
	if !redirected {
		t.Fatal("the message was not redirected")
	}
	if to != "+919799931864" {
		t.Errorf("to = %q, want the tester's number in E.164", to)
	}
}

func TestWithNoTestRecipientTheCustomerIsMessaged(t *testing.T) {
	to, redirected := personalisationRecipient("", "9136361465")
	if redirected {
		t.Error("a message was redirected with no test recipient configured")
	}
	if to != "+919136361465" {
		t.Errorf("to = %q, want the customer's own number", to)
	}
}

func TestAnUnreadableTestRecipientDoesNotSilentlyFallThrough(t *testing.T) {
	// The dangerous failure: a typo in the test number falls back to the
	// customer, and the first "test" reaches a stranger. normaliseIndianMobile
	// returns empty for rubbish, so this asserts what happens then.
	to, redirected := personalisationRecipient("not-a-number", "9136361465")
	if !redirected {
		t.Fatal("a configured test recipient did not take the message")
	}
	// Empty, so the caller sends nothing. Falling back to the customer would
	// mean the first message of a botched test run reaches a stranger - the
	// one outcome this setting exists to make impossible.
	if to != "" {
		t.Errorf("to = %q, want nothing sent at all", to)
	}
}
