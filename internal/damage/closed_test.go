package damage

// The third outcome: support settled it in conversation.
//
// It looks like the smallest of the three and it is the one most able to go
// quietly wrong. A reprint leaves a courier booking and a print job behind it;
// a reverse leaves a payment and a waybill. A closed report leaves a sentence,
// and if that sentence is empty the shop has no record of what it agreed to
// do for a customer who complained.

import "testing"

func TestClosedIsADecisionLikeTheOthers(t *testing.T) {
	if !KindClosed.Valid() {
		t.Fatal("closed is not accepted as a decision, so the dropdown cannot offer it")
	}
	// And the absence of a decision still is not one.
	if KindNone.Valid() {
		t.Error("an undecided report reads as decided")
	}
}

func TestOnlyAClosedReportDemandsItsNote(t *testing.T) {
	// The note IS the outcome here - there is no courier and nothing made, so
	// "sorted it" with no sentence is a report nobody can audit.
	if !KindClosed.NeedsNote() {
		t.Error("a closed report can be filed with no record of what was done")
	}
	// The other two are self-documenting: a waybill and a print job say what
	// happened. Demanding a note would be a box somebody types "ok" into.
	for _, k := range []Kind{KindReprint, KindReverse} {
		if k.NeedsNote() {
			t.Errorf("%q demands a note; its own artefacts already say what happened", k)
		}
	}
}

func TestAClosedReportHasItsOwnTimeline(t *testing.T) {
	plan := Plan(KindClosed)
	if len(plan) == 0 {
		t.Fatal("a closed report draws no timeline at all")
	}
	// It must not borrow another route's steps: a closed report that showed
	// "Courier booked" greyed out would have an operator waiting for a van
	// that is never coming.
	for _, step := range plan {
		switch step.Stage {
		case StageCourierBooked, StagePickupBooked, StagePaymentConfirmed, StageJobQueued:
			t.Errorf("the closed timeline promises %q, which never happens", step.Stage)
		}
	}
	if last := plan[len(plan)-1].Stage; last != StageClosed {
		t.Errorf("the closed timeline ends at %q, want %q", last, StageClosed)
	}
}

func TestTheAskIsOneMessage(t *testing.T) {
	// Choosing reverse sends the whole thing at once - explanation, QR and
	// the request for a screenshot. There is no second step where a customer
	// agrees, because paying is agreeing.
	next, send, err := Advance(Report{}, EventOffer)
	if err != nil {
		t.Fatalf("choosing reverse: %v", err)
	}
	if send != SendPaymentRequest {
		t.Errorf("sends %q, want the one combined ask %q", send, SendPaymentRequest)
	}
	// And from there the customer's next move is to pay, not to answer.
	if _, _, err := Advance(Report{Kind: KindReverse, Reverse: next}, EventPaymentClaimed); err != nil {
		t.Errorf("a customer who paid straight away was refused: %v", err)
	}
}
