package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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

// failedAt is a bed whose last send did not stick, at a given moment.
func failedAt(t time.Time) func(*gen.Batch) {
	return func(b *gen.Batch) { b.PrintErrorAt = pgtype.Timestamptz{Time: t, Valid: true} }
}

// now is the clock every case is judged against.
var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func TestNextDispatchStepHoldsADraftThatStillHasRoom(t *testing.T) {
	// Approving it would freeze a half-empty bed. The next order in the same
	// colour is meant to join it rather than open a bed of its own.
	if got := nextDispatchStep(bed(production.BatchPendingApproval), false, now); got != stepHoldOpen {
		t.Errorf("step = %v, want stepHoldOpen", got)
	}
}

func TestNextDispatchStepApprovesAFullDraft(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchPendingApproval), true, now); got != stepApprove {
		t.Errorf("step = %v, want stepApprove", got)
	}
}

func TestNextDispatchStepSendsALockedIdleBed(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchOpen), false, now); got != stepSend {
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
			if got := nextDispatchStep(c.b, false, now); got != stepNone {
				t.Errorf("step = %v, want stepNone - this bed is already on its way", got)
			}
		})
	}
}

// A failed plate must never be retried unattended, into whatever went wrong the
// first time. ListBatchesToDispatch filters these out; the walk says so too,
// because the rule is stated in three other files and none of them is the walk.
func TestNextDispatchStepNeverSendsABedWhosePrintResolved(t *testing.T) {
	if got := nextDispatchStep(bed(production.BatchOpen, withOutcome("failed")), false, now); got != stepNone {
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

// A plate that can never slice must not eat the whole pass, every pass.
//
// A failed slice releases the bed so it can be sent again - right, because the
// alternative is a bed wedged for ever. But BATCH-1002076's wipe tower collides
// with its models, so BambuBuddy answers "G-code conflicts detected" every
// time. Chosen every pass it would spend the entire per-run cap on a send that
// cannot stick, and nothing behind it would ever print.
func TestABedRestsAfterASendThatDidNotStick(t *testing.T) {
	justFailed := bed(production.BatchOpen, failedAt(now.Add(-2*time.Minute)))
	if got := nextDispatchStep(justFailed, false, now); got != stepNone {
		t.Errorf("step = %v, want stepNone - a bed that just failed must rest", got)
	}
}

// It rests, it does not stop. The shop asked for retries.
func TestABedIsSentAgainOnceItHasRested(t *testing.T) {
	rested := bed(production.BatchOpen, failedAt(now.Add(-sendCooldown-time.Minute)))
	if got := nextDispatchStep(rested, false, now); got != stepSend {
		t.Errorf("step = %v, want stepSend once the cooldown has lapsed", got)
	}
}

// The case the cooldown must NOT slow down.
//
// A bed no printer can take keeps the same reason every pass, and
// SetBatchPrintErrorIfChanged leaves the timestamp alone when the reason has
// not changed. So its error ages, the cooldown lapses, and it goes the moment
// somebody loads the spool - which is the whole behaviour the shop chose.
func TestABedWaitingOnASpoolIsNotHeldByTheCooldown(t *testing.T) {
	waiting := bed(production.BatchOpen, failedAt(now.Add(-2*time.Hour)))
	if got := nextDispatchStep(waiting, false, now); got != stepSend {
		t.Errorf("step = %v; a bed waiting hours on a spool must still be tried", got)
	}
}

// The note must name the reason that actually accounts for the bed.
//
// A printer holding this bed's colours, refused because the plate is laid out
// for a different size, is the whole story - mapping a swatch or loading a
// spool on another machine will not help. Told the colour version instead, an
// operator goes to Inventory, fixes nothing, and the bed still waits.
func TestTheNoteBlamesTheClassWhenAPrinterHoldsTheColours(t *testing.T) {
	note := noPrinterNote([]machineOption{
		// A P2S that holds the colours but cannot take an A2L plate.
		{Refusal: "this bed is laid out for a A2L", HoldsColours: true},
		// And an A2L whose spool is simply not mapped.
		{Refusal: "no tray is confirmed as RED", HoldsColours: false},
	})
	if !strings.Contains(note, "wrong size") {
		t.Errorf("note = %q; it should name the size, not send somebody to Inventory", note)
	}
}

// But a wrong-class printer that could not have printed the bed anyway
// explains nothing, and must not take the blame off a real colour problem.
func TestTheNoteStillBlamesColourWhenTheWrongClassCouldNotHavePrintedItEither(t *testing.T) {
	note := noPrinterNote([]machineOption{
		{Refusal: "this bed is laid out for a A2L", HoldsColours: false},
		{Refusal: "no tray is confirmed as RED", HoldsColours: false},
	})
	if strings.Contains(note, "wrong size") {
		t.Errorf("note = %q; the class is not the reason when that printer lacked the colour too", note)
	}
}
