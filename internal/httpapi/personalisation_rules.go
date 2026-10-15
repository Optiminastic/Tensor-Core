package httpapi

// The rules that decide whether an arriving order's customer is asked to check
// it, and what the message says.
//
// Pure, and kept apart from the worker for the same reason winbackChannels and
// eligible are: every one of these decides whether a stranger's phone buzzes,
// and that deserves tests that need no database, no Shopify and no Meta.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// matchesOrderNumber reports whether the allow-list names this order.
//
// Compared loosely - case-insensitively, ignoring surrounding space and a
// leading '#' on either side - because the value is typed into an environment
// variable by a person reading an order number off a screen, where it is
// printed as "#66897853350101". An allow-list that silently matches nothing
// because of a hash is a testing gate that looks armed and is not.
func matchesOrderNumber(only []string, orderNumber string) bool {
	want := normaliseOrderNumber(orderNumber)
	if want == "" {
		return false
	}
	for _, candidate := range only {
		if normaliseOrderNumber(candidate) == want {
			return true
		}
	}
	return false
}

func normaliseOrderNumber(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "#")
}

// orderNeedsPersonalisation reports whether anything on the order was made to
// the customer's own instructions.
//
// Read from the order's own line items rather than from its production jobs,
// because this runs the instant the order lands and the jobs are created by a
// different worker - both triggers are enqueued in the same transaction and
// neither is promised to run first. The line items are already here.
func orderNeedsPersonalisation(order gen.Order) bool {
	for _, li := range orderLineItems(order) {
		if lineIsPersonalised(li) {
			return true
		}
	}
	return false
}

// lineIsPersonalised reports whether the customer typed anything on this line
// that they could have got wrong.
//
// NOT PersonalisationRequired ALONE, and that flag is why this function
// exists. The importer sets it by matching each property's label against a
// hard-coded list of spellings - "step 4 first name", "engraving_text",
// "custom_name" - which the Dual Name Plank happens to use and nothing else
// does. A Soulmate Combo asks "STEP 2 - First Name-" and "Name On 3D Rose";
// a DUOSNAP asks "TOP NAME" and "BOTTOM NAME". None of those match, so the
// flag is false, so the order reads as "nothing on this order is
// personalised" and its customer is never asked to check the names that will
// be printed on their gift.
//
// Measured on the live store: T3DPS-115684 and T3DPS-115768 both carry names
// the customer typed and both were refused for having nothing personalised.
// A whitelist of labels cannot keep up with a storefront that renames its own
// questions, and the failure is silent - the order simply goes to print.
//
// So the question is asked of the data instead: does this line carry a
// property a person filled in? That is the same test the customer's own page
// makes when it decides what to show them (see publicItem), which is the
// point - if the page would offer a field to check, the customer should be
// asked to check it.
//
// The flag is still honoured first. It carries lines whose personalisation
// arrived as a structured field rather than as a property, and dropping it
// would narrow the rule while widening it.
func lineIsPersonalised(li production.LineItem) bool {
	if li.PersonalisationRequired {
		return true
	}
	for _, p := range li.Properties {
		// Shopify's own bookkeeping, an empty answer, and a contact detail
		// are each things the page will not show - so none of them is
		// something to ask a customer to check.
		if p.Hidden() || strings.TrimSpace(p.Value) == "" || contactProperty(p.Name) {
			continue
		}
		return true
	}
	return false
}

// orderLineItems decodes the order's stored line items, or nothing.
//
// A decode failure is treated as "no line items" rather than as an error: the
// only caller is deciding whether to message somebody, and the safe answer to
// "I cannot read this order" is to leave the customer alone.
func orderLineItems(order gen.Order) []production.LineItem {
	if len(order.LineItems) == 0 {
		return nil
	}
	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return nil
	}
	return items
}

// orderPlacedAt is when the customer paid, falling back to when Tensor saw it.
//
// The two differ by more than they look. Orders are pulled, never pushed, so
// imported_at is when a sync happened to run; placed_at is the moment the
// customer actually bought. The watermark compares against the customer's
// moment, so switching this on cannot message somebody whose order was placed
// last week and imported this morning.
func orderPlacedAt(order gen.Order) time.Time {
	if order.PlacedAt.Valid {
		return order.PlacedAt.Time
	}
	if order.ImportedAt.Valid {
		return order.ImportedAt.Time
	}
	return time.Time{}
}

// personalisationBodyParams is what the approved template says, in its order:
// the customer's name, their order number, and how many items are on it.
//
// EVERY ONE HAS A NON-EMPTY FALLBACK. Meta refuses an empty template
// parameter outright and refuses the whole message with it, and a missing
// customer name is ordinary - a Shopify order can reach payment with an email
// and nothing else.
func personalisationBodyParams(order gen.Order) []string {
	name := oneLine(deref(order.CustomerName))
	if name == "" {
		name = "there"
	}

	number := oneLine(order.OrderNumber)
	if number == "" {
		number = "your order"
	}

	return []string{name, number, spokenItemCount(personalisedItemCount(order))}
}

// personalisedItemCount counts the units the customer can actually change.
//
// Units, not lines: three of the same plank is three things to check, and a
// message saying "1 item" about three planks reads as though the shop has lost
// two of them.
func personalisedItemCount(order gen.Order) int {
	total := 0
	for _, li := range orderLineItems(order) {
		// The same test that decided to send the message at all. Counting on
		// the raw flag here would tell a customer with three personalised
		// items that they have one - spokenItemCount clamps zero up to one -
		// which is worse than not counting at all.
		if !lineIsPersonalised(li) {
			continue
		}
		if li.Quantity > 0 {
			total += li.Quantity
			continue
		}
		total++
	}
	return total
}

// spokenItemCount renders the count as the phrase the message reads out.
//
// THE NOUN COMES WITH THE NUMBER rather than being written into the template,
// because the template cannot agree with itself about the plural. A template
// reading "your {{3}} personalised items" says "your 1 personalised items" to
// every single-item order - which is most of them - and "item(s)" is the
// usual way out of that, which reads like a form rather than a message. The
// parameter carries "1 personalised item" or "3 personalised items" and the
// template simply emphasises it.
//
// Never "0": a message telling somebody there is nothing to check would not
// have been sent at all, so a count that arrives as zero is a bug upstream
// and one item is the safe thing to say.
func spokenItemCount(n int) string {
	if n < 1 {
		n = 1
	}
	if n == 1 {
		return "1 personalised item"
	}
	return strconv.Itoa(n) + " personalised items"
}

// timestamptzAt is db.Timestamptz for a value rather than a pointer, so a
// caller computing an expiry inline does not have to name it first.
func timestamptzAt(t time.Time) pgtype.Timestamptz {
	return db.Timestamptz(&t)
}

// personalisationRecipient decides which phone actually receives the message.
//
// WHILE A TEST RECIPIENT IS CONFIGURED, NOBODY ELSE CAN BE REACHED. Not a
// filter that lets some customers through - a redirect that takes all of them.
// Testing against a live shop means testing against real people's orders, and
// the only safe way to do that is for every message to arrive on one phone
// that belongs to whoever is testing.
//
// Returns the number to message and whether it was redirected, so the caller
// can say so in the log and in the ledger rather than recording a message to
// a customer who never got one.
// FAILS CLOSED ON A BAD TEST NUMBER. If the setting is present but cannot be
// read as a phone number - a typo, a stray quote, a number pasted with a
// label - this returns an empty recipient and the caller sends nothing. The
// alternative, falling back to the customer, means the very first message of
// a botched test run reaches a stranger, which is the one outcome this
// setting exists to make impossible.
func personalisationRecipient(configured, customer string) (to string, redirected bool) {
	if strings.TrimSpace(configured) != "" {
		// Redirected either way: a configured test recipient means no customer
		// is reachable, whether or not the number turns out to be usable.
		return normaliseIndianMobile(configured), true
	}
	return normaliseIndianMobile(customer), false
}
