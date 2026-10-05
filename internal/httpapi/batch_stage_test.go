package httpapi

// The stage shown on the board has to be the truth about the bed.
//
// It is derived from the dispatcher's own marker columns, so the risk is not
// that it renders wrongly - it is that it and nextDispatchStep read the same
// row and come to different conclusions. That is the failure these pin: a bed
// the dispatcher is actively slicing must never read "Waiting", and a bed the
// dispatcher is retrying every pass must never read "Held up".

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestBatchStageFollowsTheBedFromDraftToDone(t *testing.T) {
	for _, c := range []struct {
		name  string
		bed   gen.Batch
		want  batchStage
		label string
	}{
		{
			name:  "a Draft is still collecting planks",
			bed:   bed(production.BatchPendingApproval),
			want:  stageDraft,
			label: "Collecting planks",
		},
		{
			// The honest answer for a locked bed nothing has happened to yet.
			// It is not stuck and it is not working; its turn has not come, or
			// no printer is free.
			name:  "locked with no markers is waiting for a printer",
			bed:   bed(production.BatchOpen),
			want:  stageWaiting,
			label: "Waiting for a printer",
		},
		{
			// The stretch that prompted all of this. Minutes long, and the
			// board used to say LOCKED throughout it.
			name:  "a slice job means BambuBuddy has the plate",
			bed:   bed(production.BatchOpen, withSliceJob(77)),
			want:  stageSlicing,
			label: "Slicing…",
		},
		{
			// Same evidence on the older path, and just as much "not idle".
			name: "a pipeline run is the same evidence",
			bed:  bed(production.BatchOpen, withPipelineRun(12)),
			want: stageSlicing,
		},
		{
			name:  "a queue item means it is on the printer's queue",
			bed:   bed(production.BatchOpen, withQueueItem(204)),
			want:  stageQueued,
			label: "Queued on the printer",
		},
		{
			name:  "in_progress is printing",
			bed:   bed(production.BatchInProgress),
			want:  stagePrinting,
			label: "Printing…",
		},
		{
			name:  "completed is done",
			bed:   bed(production.BatchCompleted),
			want:  stageDone,
			label: "Done",
		},
		{
			name:  "a fresh failure is held up",
			bed:   bed(production.BatchOpen, failedAt(now.Add(-time.Minute))),
			want:  stageBlocked,
			label: "Held up",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := batchStageOf(c.bed, now)
			if got != c.want {
				t.Fatalf("stage = %q, want %q", got, c.want)
			}
			if c.label != "" && stageLabel[got] != c.label {
				t.Errorf("label = %q, want %q", stageLabel[got], c.label)
			}
			if stageLabel[got] == "" {
				t.Errorf("stage %q has no label; the board would render an empty cell", got)
			}
		})
	}
}

// A bed waiting hours on a spool is WAITING, not held up.
//
// Its reason stays on the row - SetBatchPrintErrorIfChanged leaves the
// timestamp alone while the reason is unchanged - so the error ages out of the
// cooldown and the bed is tried again on every pass. Calling that "Held up"
// would put the word on the one bed where it is least true, and leave an
// operator thinking Tensor had given up on a plate it retries every seven
// minutes.
//
// This is the same row nextDispatchStep answers stepSend for; see
// TestABedWaitingOnASpoolIsNotHeldByTheCooldown.
func TestAnAgedFailureReadsAsWaitingBecauseItIsBeingRetried(t *testing.T) {
	old := bed(production.BatchOpen, failedAt(now.Add(-2*time.Hour)))
	if got := batchStageOf(old, now); got != stageWaiting {
		t.Errorf("stage = %q, want %q; this bed is tried on every pass", got, stageWaiting)
	}
}

// Markers win over a stale reason, because one is happening and one happened.
//
// A bed that failed, rested, and has since been picked up again carries both
// for as long as the new attempt runs. "Slicing…" is what it is doing; the old
// reason is still available for the detail line.
func TestAnAttemptInFlightOutranksTheReasonTheLastOneFailed(t *testing.T) {
	retrying := bed(production.BatchOpen, failedAt(now.Add(-time.Minute)), withSliceJob(91))
	if got := batchStageOf(retrying, now); got != stageSlicing {
		t.Errorf("stage = %q, want %q; the bed is being sliced right now", got, stageSlicing)
	}
}

// The stage must never contradict what the dispatcher will actually do.
//
// Not every pairing is meaningful - a Draft's step depends on readyToLock,
// which the stage has no business knowing - but these three are, and they are
// the ones an operator reads off the board and then waits on.
func TestTheStageAgreesWithWhatTheDispatcherWillDo(t *testing.T) {
	for _, c := range []struct {
		name     string
		bed      gen.Batch
		stage    batchStage
		wantStep dispatchStep
	}{
		{
			// Shown as working, and the walk leaves it alone. Both right.
			name: "slicing", bed: bed(production.BatchOpen, withSliceJob(5)),
			stage: stageSlicing, wantStep: stepNone,
		},
		{
			name: "queued", bed: bed(production.BatchOpen, withQueueItem(9)),
			stage: stageQueued, wantStep: stepNone,
		},
		{
			// Shown as held up, and the walk is indeed resting it.
			name: "blocked", bed: bed(production.BatchOpen, failedAt(now.Add(-time.Minute))),
			stage: stageBlocked, wantStep: stepNone,
		},
		{
			// Shown as waiting, and the walk will send it. This is the pairing
			// that matters most: "Waiting for a printer" is a promise.
			name: "waiting", bed: bed(production.BatchOpen),
			stage: stageWaiting, wantStep: stepSend,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := batchStageOf(c.bed, now); got != c.stage {
				t.Fatalf("stage = %q, want %q", got, c.stage)
			}
			if got := nextDispatchStep(c.bed, false, now); got != c.wantStep {
				t.Errorf("the board says %q while the dispatcher would do %v, want %v",
					c.stage, got, c.wantStep)
			}
		})
	}
}
