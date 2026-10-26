package damage

import (
	"errors"
	"strings"
	"testing"
)

// The whole sequence, in the order it happens to a real customer.
func TestTheReverseFlowRunsEndToEnd(t *testing.T) {
	r := Report{Kind: KindReverse}

	for _, step := range []struct {
		ev    Event
		state ReverseState
		send  Outbound
	}{
		{EventOffer, StateOffered, SendPaymentRequest},
		{EventPaymentClaimed, StatePaymentClaimed, SendNothing},
		{EventConfirmPayment, StatePaid, SendNothing},
		{EventBookPickup, StatePickupBooked, SendPickupBooked},
	} {
		next, send, err := Advance(r, step.ev)
		if err != nil {
			t.Fatalf("%s: %v", step.ev, err)
		}
		if next != step.state {
			t.Fatalf("%s -> %s, want %s", step.ev, next, step.state)
		}
		if send != step.send {
			t.Errorf("%s sends %q, want %q", step.ev, send, step.send)
		}
		r.Reverse = next
	}
}

// THE RULE THAT PROTECTS THE MONEY.
//
// A pickup costs the shop a courier and sends a van to somebody's door. It is
// bookable only from paid - never from one where the customer has only been
// ASKED, and never from one where they have only SAID they paid. All three sit
// a click apart on a board where the rows look alike.
func TestAPickupIsOnlyBookableOnceThePaymentIsConfirmed(t *testing.T) {
	for _, from := range []ReverseState{
		StateNone, StateOffered, StatePaymentClaimed,
	} {
		_, send, err := Advance(Report{Kind: KindReverse, Reverse: from}, EventBookPickup)
		if err == nil {
			t.Errorf("a pickup was bookable from %q", from)
			continue
		}
		if send != SendNothing {
			t.Errorf("a refused booking from %q still sent %q", from, send)
		}
		// The refusal has to say what to do next, because it is the entire
		// explanation an operator gets.
		if !strings.Contains(err.Error(), "payment is confirmed") {
			t.Errorf("refusal from %q reads %q, which does not say why", from, err)
		}
	}
}

// A screenshot is not proof, and the two states must stay apart.
//
// payment_claimed is "they say they paid"; paid is "we have the money". If
// claiming collapsed into paid, a customer could book their own pickup by
// sending any picture at all.
func TestAClaimedPaymentIsNotAConfirmedOne(t *testing.T) {
	next, _, err := Advance(Report{Kind: KindReverse, Reverse: StateOffered}, EventPaymentClaimed)
	if err != nil {
		t.Fatalf("payment claim: %v", err)
	}
	if next == StatePaid {
		t.Fatal("a screenshot moved the report straight to paid; a person has to look at it")
	}
	if _, _, err := Advance(Report{Kind: KindReverse, Reverse: next}, EventBookPickup); err == nil {
		t.Fatal("a pickup was bookable off a screenshot alone")
	}
}

// Support may confirm a payment they can see in the bank even if no screenshot
// ever arrived. The screenshot is evidence, not a gate.
func TestPaymentCanBeConfirmedWithoutAScreenshot(t *testing.T) {
	next, _, err := Advance(Report{Kind: KindReverse, Reverse: StateOffered}, EventConfirmPayment)
	if err != nil {
		t.Fatalf("confirming a payment with no screenshot: %v", err)
	}
	if next != StatePaid {
		t.Errorf("state = %q, want %q", next, StatePaid)
	}
}

// Customers send two screenshots. That is not a mistake, and it should not
// produce an error on their phone or move the report.
func TestRepeatedCustomerActionsAreNotErrors(t *testing.T) {
	next, send, err := Advance(
		Report{Kind: KindReverse, Reverse: StatePaymentClaimed}, EventPaymentClaimed)
	if err != nil {
		t.Errorf("a second screenshot errored: %v", err)
	}
	if next != StatePaymentClaimed || send != SendNothing {
		t.Errorf("a second screenshot moved things: %q / %q", next, send)
	}
}

// The offer is the decision, so it is the one event allowed before a kind is
// set - and it must not be repeatable.
func TestAnOfferCannotBeRepeated(t *testing.T) {
	next, send, err := Advance(Report{}, EventOffer)
	if err != nil {
		t.Fatalf("offering an undecided report: %v", err)
	}
	if next != StateOffered || send != SendPaymentRequest {
		t.Fatalf("offer -> %q / %q", next, send)
	}

	for _, from := range []ReverseState{
		StateOffered, StatePaymentClaimed, StatePaid, StatePickupBooked,
	} {
		if _, _, err := Advance(Report{Kind: KindReverse, Reverse: from}, EventOffer); err == nil {
			t.Errorf("the offer was repeatable from %q, re-asking a customer who answered", from)
		}
	}
}

// Reverse events on a reprint are refused distinctly, because the fix is
// different: somebody is acting on the wrong report.
func TestReverseEventsAreRefusedOnAReprint(t *testing.T) {
	for _, ev := range []Event{
		EventPaymentClaimed, EventConfirmPayment, EventBookPickup,
	} {
		_, _, err := Advance(Report{Kind: KindReprint, Reverse: StateNone}, ev)
		if !errors.Is(err, ErrNotReverse) {
			t.Errorf("%s on a reprint gave %v, want ErrNotReverse", ev, err)
		}
	}
}

// Every state a refusal can name has wording written for somebody reading a
// board. A bare state name in an error is how an operator ends up asking what
// "agreed" means.
func TestEveryStateDescribesItselfInWords(t *testing.T) {
	for _, s := range []ReverseState{
		StateNone, StateOffered, StatePaymentClaimed, StatePaid, StatePickupBooked,
	} {
		got := Describe(s)
		if got == "" || got == string(s) {
			t.Errorf("state %q describes itself as %q", s, got)
		}
	}
}
