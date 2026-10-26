// Package damage holds the rules for what the shop does about a damaged
// parcel. Pure: states in, next state and the thing to send out, no database
// and no HTTP, so the sequence can be tested without a report, a customer or
// a courier.
//
// There are three outcomes and they are not alike. A REPRINT costs the
// customer nothing - we make it again and send it. A REVERSE asks them for Rs
// 100 for the leg from their home to the warehouse, which turns a support
// decision into a negotiation spread over days: we ask, the customer pays, a
// person checks the payment, and an admin books the courier. Every one of
// those can be the step everyone is waiting on, which is why the progression
// below is a state and not a boolean. A CLOSED report is one support settled
// in conversation - a missing screw posted on, a mark that buffs out - and it
// ends there, with a note saying what was done.
//
// THE THREE HUMAN STEPS ARE DELIBERATELY MANUAL, at the shop's instruction:
// deciding the outcome, confirming the money arrived, and booking the pickup.
// Nothing here fires a courier or settles a payment on its own.
//
// THE ASK IS ONE MESSAGE. It used to be two - an offer with Yes/No buttons,
// then the QR once they agreed - and the shop asked for them combined: the
// explanation, the QR and "send a screenshot" together. So there is no
// agreement to record any more. Paying IS agreeing, and a customer who does
// not want a reverse pickup simply does not pay.
package damage

import "fmt"

// Kind is what the shop decided to do about a report.
type Kind string

const (
	// KindNone is a report nobody has decided about yet.
	KindNone Kind = ""
	// KindReprint - we make it again and send it, at our cost.
	KindReprint Kind = "reprint"
	// KindReverse - the customer pays Rs 100 for the leg to the warehouse, we
	// repair it, and we pay the leg home.
	KindReverse Kind = "reverse"
	// KindClosed - support settled it in conversation and there is nothing to
	// make, send or collect.
	//
	// It is a real decision rather than an absence of one. Without it the
	// conversations support actually has - a replacement screw in an
	// envelope, a mark that comes off - end with a report sitting open for
	// ever or a reprint nobody needed.
	//
	// The NOTE is what this outcome is. The others leave a courier booking
	// and a print job behind them; this one leaves a sentence, so the handler
	// refuses it without one.
	KindClosed Kind = "closed"
)

// Valid reports whether k is a decision this package knows how to carry out.
func (k Kind) Valid() bool {
	return k == KindReprint || k == KindReverse || k == KindClosed
}

// NeedsNote reports whether this decision is meaningless without one.
func (k Kind) NeedsNote() bool { return k == KindClosed }

// ReverseState is how far the reverse negotiation has got.
type ReverseState string

const (
	// StateNone - not a reverse report, or not offered yet.
	StateNone ReverseState = ""
	// StateOffered - the Rs 100 ask and the QR have gone, and we are waiting
	// on the money.
	//
	// Named "offered" because that is what the column has always held and
	// renaming a stored value costs a migration for a word. It means "asked,
	// waiting to be paid".
	StateOffered ReverseState = "offered"
	// StatePaymentClaimed - they sent a screenshot. NOBODY HAS CHECKED IT.
	//
	// Its own state rather than folded into paid, because a screenshot is a
	// picture of a screen and not proof. A person looking at it is the whole
	// reason support is in this flow, and the board has to be able to show
	// "they say they paid" differently from "we have the money".
	StatePaymentClaimed ReverseState = "payment_claimed"
	// StatePaid - a person looked at the screenshot and confirmed it.
	StatePaid ReverseState = "paid"
	// StatePickupBooked - an admin booked the reverse pickup with Delhivery.
	StatePickupBooked ReverseState = "pickup_booked"
)

// Report is the part of a damage report these rules read.
type Report struct {
	Kind    Kind
	Reverse ReverseState
}

// Event is something that happened to a report.
type Event string

const (
	// EventOffer - an admin chose the reverse route, which sends the ask.
	EventOffer Event = "offer"
	// EventPaymentClaimed - the customer sent something that looks like proof.
	EventPaymentClaimed Event = "payment_claimed"
	// EventConfirmPayment - support looked at it and confirmed the money.
	EventConfirmPayment Event = "confirm_payment"
	// EventBookPickup - an admin booked the courier.
	EventBookPickup Event = "book_pickup"
)

// Outbound is a message the shop should send as a result of an event. Named
// rather than built here: this package decides WHETHER to send, and
// whatsappbot/content decides what the words are.
type Outbound string

const (
	// SendNothing - the step is internal, or the customer has already been
	// told what they need to know.
	SendNothing Outbound = ""
	// SendPaymentRequest is the whole ask in one message: what a reverse
	// pickup is, what the customer pays, what the shop pays, the QR, and the
	// request for a screenshot.
	SendPaymentRequest Outbound = "payment_request"
	// SendPickupBooked tells the customer the courier is coming.
	SendPickupBooked Outbound = "pickup_booked"
)

// ErrNotReverse is returned for a reverse-flow event on a report that is not
// a reverse. Separated from a wrong-order error because the fix differs: this
// one means somebody is acting on the wrong report.
var ErrNotReverse = fmt.Errorf("this report is not a reverse pickup")

// Advance applies an event to a report and says what the next state is and
// what to send.
//
// EVERY REFUSAL NAMES THE STATE IT IS IN. "cannot confirm payment" tells a
// person nothing; "cannot confirm payment: the customer has not agreed yet"
// tells them to go and look at the chat. These errors reach the operator
// through the API's detail string, so they are the whole explanation.
//
// Transitions are STRICT and never skip. Booking a pickup for a parcel nobody
// has paid for, or sending a QR to somebody who never agreed, are exactly the
// mistakes a state machine exists to stop - and both are easy to make from a
// board where the rows look alike.
func Advance(r Report, ev Event) (ReverseState, Outbound, error) {
	if ev == EventOffer {
		// The only event that is allowed before a decision exists: it IS the
		// decision. Re-offering a report already past the offer would drag it
		// backwards and re-ask a customer who has already answered.
		if r.Reverse != StateNone {
			return r.Reverse, SendNothing, fmt.Errorf(
				"this customer has already been asked for the reverse pickup (%s)", describe(r.Reverse))
		}
		return StateOffered, SendPaymentRequest, nil
	}

	if r.Kind != KindReverse {
		return r.Reverse, SendNothing, ErrNotReverse
	}

	switch ev {
	case EventPaymentClaimed:
		// From offered, or from payment_claimed again: a customer who sends a
		// second screenshot has not done anything wrong, and the report
		// should not move or complain.
		switch r.Reverse {
		case StateOffered, StatePaymentClaimed:
			return StatePaymentClaimed, SendNothing, nil
		default:
			return r.Reverse, SendNothing, fmt.Errorf(
				"cannot attach payment proof here: this report is %s", describe(r.Reverse))
		}

	case EventConfirmPayment:
		// From payment_claimed, and ALSO from offered. The customer may pay
		// and never send a screenshot, or send it somewhere nobody linked -
		// and support confirming money they can see in the bank is the point
		// of this step. Refusing that would make the screenshot mandatory,
		// which it is not; it is evidence, not a gate.
		switch r.Reverse {
		case StateOffered, StatePaymentClaimed:
			return StatePaid, SendNothing, nil
		case StatePaid:
			return r.Reverse, SendNothing, fmt.Errorf(
				"this payment is already confirmed")
		default:
			return r.Reverse, SendNothing, fmt.Errorf(
				"cannot confirm a payment here: this report is %s", describe(r.Reverse))
		}

	case EventBookPickup:
		// ONLY from paid. This is the step that costs money and sends a
		// courier to somebody's door; it is the one transition that must
		// never be reachable by a tired person clicking down a list.
		if r.Reverse != StatePaid {
			return r.Reverse, SendNothing, fmt.Errorf(
				"cannot book the pickup: this report is %s, and a pickup is only "+
					"booked once the payment is confirmed", describe(r.Reverse))
		}
		return StatePickupBooked, SendPickupBooked, nil
	}

	return r.Reverse, SendNothing, fmt.Errorf("unknown event %q", ev)
}

// describe is how a state is named in a refusal the operator reads. Written
// for somebody looking at a board, not for a log: "waiting for the customer to
// agree" says what to do next, and "offered" does not.
func describe(s ReverseState) string {
	switch s {
	case StateNone:
		return "not asked for a reverse pickup"
	case StateOffered:
		return "waiting for the customer to pay"
	case StatePaymentClaimed:
		return "waiting for somebody to check the payment"
	case StatePaid:
		return "paid, and waiting for the pickup to be booked"
	case StatePickupBooked:
		return "already booked for pickup"
	default:
		return string(s)
	}
}

// Describe is describe, for callers outside this package that want the same
// wording on a screen as in a refusal.
func Describe(s ReverseState) string { return describe(s) }
