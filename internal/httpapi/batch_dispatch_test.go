package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bed builds a batch row in one of the states the walk has to tell apart.
func bed(status string, set ...func(*gen.Batch)) gen.Batch {
	b := gen.Batch{ID: uuid.New(), BatchNumber: "BATCH-1", Status: status}
	for _, f := range set {
		f(&b)
	}
	return b
}

func withQueueItem(id int32) func(*gen.Batch)   { return func(b *gen.Batch) { b.QueueItemID = &id } }
func withSliceJob(id int32) func(*gen.Batch)    { return func(b *gen.Batch) { b.BambuSliceJobID = &id } }
func withPipelineRun(id int32) func(*gen.Batch) { return func(b *gen.Batch) { b.PipelineRunID = &id } }
func withOutcome(v string) func(*gen.Batch)     { return func(b *gen.Batch) { b.PrintOutcome = &v } }

func TestNextDispatchStepHoldsADraftThatStillHasRoom(t *testing.T) {
	// Approving it would freeze a half-empty bed. The next order in the same
	// colour is meant to join it rather than open a bed of its own.
	if got := nextDispatchStep(bed(production.BatchPendingApproval), false); got != stepHoldOpen {
		t.Errorf("step = %v, want stepHoldOpen", got)
	}
}

func TestNextDispatchStepApprovesAFullDraft(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchPendingApproval), true); got != stepApprove {
		t.Errorf("step = %v, want stepApprove", got)
	}
}

func TestNextDispatchStepSendsALockedIdleBed(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchOpen), false); got != stepSend {
		t.Errorf("step = %v, want stepSend", got)
	}
}

// The bug this function exists to kill.
//
// A slice takes minutes and queue_item_id does not appear until it lands, so
// reading that column alone sent every mid-slice bed into the send branch. The
// already-sent guard turned each into a no-op that still counted as work, and
// with the default cap of five, five beds slicing meant a pass that did nothing
// at all - and kept doing nothing until they landed.
func TestNextDispatchStepSkipsABedThatIsAlreadyMoving(t *testing.T) {
	for _, c := range []struct {
		name string
		b    gen.Batch
	}{
		{"queued", bed(production.BatchOpen, withQueueItem(7))},
		{"mid-slice", bed(production.BatchOpen, withSliceJob(42))},
		{"pipeline run", bed(production.BatchOpen, withPipelineRun(9))},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := nextDispatchStep(c.b, false); got != stepNone {
				t.Errorf("step = %v, want stepNone - this bed is already on its way", got)
			}
		})
	}
}

// A failed plate must never be retried unattended, into whatever went wrong the
// first time. ListBatchesToDispatch filters these out; the walk says so too,
// because the rule is stated in three other files and none of them is the walk.
func TestNextDispatchStepNeverSendsABedWhosePrintResolved(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchOpen, withOutcome("failed")), false); got != stepNone {
		t.Errorf("step = %v, want stepNone", got)
	}
}

// What reaches the Batches page as "Not sent: …" must be the sentence that says
// what to do, never the plumbing underneath it.
func TestReasonOfNeverLeaksTheCause(t *testing.T) {
	err := statusErrf(http.StatusInternalServerError,
		"Could not read the batch's jobs.",
		errors.New("dial tcp 10.0.0.4:5432: i/o timeout"))

	if got := reasonOf(err); got != "Could not read the batch's jobs." {
		t.Errorf("reason = %q; an operator must not be shown the dial error", got)
	}
}

func TestReasonOfCarriesTheNoPrinterNote(t *testing.T) {
	const note = "No printer has this bed's colours loaded. Load a spool, or wait for one to free up."
	err := statusErrf(http.StatusConflict, note, errNoPrinter)

	if !errors.Is(err, errNoPrinter) {
		t.Error("the no-printer sentinel did not survive wrapping; the dispatcher would count it as a fault")
	}
	if got := reasonOf(err); got != note {
		t.Errorf("reason = %q, want the note naming the fix", got)
	}
}

func TestReasonOfFallsBackRatherThanSayingNothing(t *testing.T) {
	if got := reasonOf(errors.New("some unexpected thing")); got == "" {
		t.Error("an unexpected error produced an empty reason; the bed would show a blank note")
	}
}

// The rule three separate files state in prose and nothing enforced in code.
//
// A bed whose print failed keeps its outcome, and clearing it is the deliberate
// human "run that again". prepareBatchForQueue cleared it for ANY caller, and
// only ListBatchesToDispatch's filter - in a different file - kept the
// dispatcher away from one. That is an invariant held together by a WHERE
// clause somebody could reasonably change.
func TestAnAutomaticSendRefusesABedWhosePrintFailed(t *testing.T) {
	s := &Server{}
	failed := bed(production.BatchOpen, withOutcome("failed"))

	_, locked, err := s.prepareBatchForQueue(t.Context(), failed, systemActor, automaticSend)
	if err == nil {
		t.Fatal("the dispatcher was allowed to re-send a failed plate")
	}
	if locked {
		t.Error("a refused bed was reported as locked")
	}
	// Named, so whoever reads it knows the bed is waiting on a decision rather
	// than on a printer.
	var se *statusError
	if !errors.As(err, &se) || se.status != http.StatusConflict {
		t.Errorf("err = %v, want a 409 naming the failed print", err)
	}
}

// An operator may do exactly what the dispatcher may not: that IS the "run it
// again" decision. Guarded so the refusal above cannot quietly become a refusal
// for everyone, which would leave a failed bed unprintable by any route.
func TestAnOperatorMayStillRerunAFailedBed(t *testing.T) {
	s := &Server{}
	failed := bed(production.BatchOpen, withOutcome("failed"))

	// It gets past the origin gate and on to the database, which this Server
	// has none of - so a panic or a DB error both mean the gate let it
	// through, and only a 409 would mean it did not.
	defer func() { _ = recover() }()
	_, _, err := s.prepareBatchForQueue(t.Context(), failed, "a-real-person", operatorSend)

	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusConflict {
		t.Error("an operator was refused a re-run; a failed bed would be unprintable by any route")
	}
}
