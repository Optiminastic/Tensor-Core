package damage

import "testing"

// The reprint sequence, in the order the shop asked for it.
//
// THE COURIER BEFORE THE MESSAGE is the one ordering that matters: telling a
// customer their replacement is on the way and then failing to book a courier
// leaves the shop having promised what it has not arranged.
func TestTheCourierIsBookedBeforeTheCustomerIsTold(t *testing.T) {
	var courier, told int
	for i, step := range Plan(KindReprint) {
		switch step.Stage {
		case StageCourierBooked:
			courier = i
		case StageCustomerTold:
			told = i
		}
	}
	if courier == 0 || told == 0 {
		t.Fatal("the reprint plan is missing the courier or the message")
	}
	if courier > told {
		t.Error("the customer is told before the courier is booked, so the shop " +
			"can promise a replacement it has not arranged")
	}
}

// A reverse books no forward courier and queues no print. Those steps must not
// appear in its plan, or every reverse would read as permanently unfinished.
func TestAReversePlanHasNoPrintSteps(t *testing.T) {
	for _, step := range Plan(KindReverse) {
		switch step.Stage {
		case StageCourierBooked, StageJobQueued, StagePrinting:
			t.Errorf("the reverse plan includes %q, which only a reprint does", step.Stage)
		}
	}
}

// An undecided report still has a plan: reported, and waiting on somebody.
func TestAnUndecidedReportShowsWhatItIsWaitingFor(t *testing.T) {
	plan := Plan(KindNone)
	if len(plan) != 2 || plan[1].Stage != StageDecided {
		t.Fatalf("plan = %v, want it to end at the decision", plan)
	}
	if plan[1].Label == "" {
		t.Error("the waiting step has no label, so the drawer shows a blank row")
	}
}

// A FAILURE IS NOT PROGRESS.
//
// Rows arrive in the order things happened, so a later step that failed would
// make the report look further along than it is if the furthest stage were
// read off the newest row.
func TestAFailedLaterStepDoesNotAdvanceTheReport(t *testing.T) {
	got := Furthest(KindReprint, map[Stage]State{
		StageReported:      StateDone,
		StageDecided:       StateDone,
		StageCourierBooked: StateFailed,
		StageCustomerTold:  StateFailed,
	})
	if got != StageDecided {
		t.Errorf("furthest = %q, want %q - neither failed step is progress", got, StageDecided)
	}
}

// The EARLIEST failure is the one to name.
//
// A courier that would not take the shipment is WHY the customer was never
// told; naming the second failure sends somebody to fix a symptom.
func TestTheEarliestFailureIsWhatBlocksTheReport(t *testing.T) {
	step, blocked := Blocked(KindReprint, map[Stage]State{
		StageReported:      StateDone,
		StageDecided:       StateDone,
		StageCourierBooked: StateFailed,
		StageCustomerTold:  StateFailed,
	})
	if !blocked {
		t.Fatal("a report with two failed steps reported nothing blocking it")
	}
	if step.Stage != StageCourierBooked {
		t.Errorf("blocked at %q, want %q - the later failure is a symptom",
			step.Stage, StageCourierBooked)
	}
}

// A healthy report is not blocked, and a skipped step is not a failure.
func TestSkippedIsNotBlocked(t *testing.T) {
	if _, blocked := Blocked(KindReverse, map[Stage]State{
		StageReported:      StateDone,
		StageDecided:       StateDone,
		StageCourierBooked: StateSkipped,
	}); blocked {
		t.Error("a skipped step was reported as blocking the report")
	}
}

// Every step a plan names has to be readable on screen. A blank label is a
// blank row in the drawer.
func TestEveryStepIsLabelled(t *testing.T) {
	for _, kind := range []Kind{KindNone, KindReprint, KindReverse} {
		seen := map[Stage]bool{}
		for _, step := range Plan(kind) {
			if step.Label == "" {
				t.Errorf("%s: stage %q has no label", kind, step.Stage)
			}
			if seen[step.Stage] {
				t.Errorf("%s: stage %q appears twice, so the timeline repeats itself",
					kind, step.Stage)
			}
			seen[step.Stage] = true
		}
	}
}
