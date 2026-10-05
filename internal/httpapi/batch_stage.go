package httpapi

// Where a bed actually is, in words, between being locked and coming off the
// printer.
//
// batches.status has four values and three of them cover the whole journey from
// "a printer has been chosen" to "it is printing": pending_approval, open,
// in_progress, completed. A bed sits at 'open' while its plate uploads, while
// BambuBuddy slices it (minutes), while it waits in that printer's queue, and
// while it waits for a spool nobody has loaded. Four different situations, one
// word on screen - so the board said LOCKED for twenty minutes and an operator
// had no way to tell a bed that was working from a bed that was stuck.
//
// The information was always there. It lives in the marker columns the
// dispatcher already reads - bambu_slice_job_id, pipeline_run_id, queue_item_id,
// print_error - and nothing ever showed them. This derives a stage from exactly
// those columns, so the stage cannot claim anything the dispatcher would not
// act on.
//
// Deliberately NOT a new status column, and not new values in the existing one.
// batches.status is what the Kanban board's four columns are, and cards are
// dragged between them in both directions; adding 'slicing' would either create
// a column nobody wants or make a card droppable into a state no operator can
// put it in. A stage is read-only, derived on every response, and impossible to
// get out of step with the row.

import (
	"time"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// batchStage is the machine-readable stage. The frontend styles on this; the
// label is what it shows when it has nothing better.
type batchStage string

const (
	// stageDraft: still collecting planks. Not locked, nothing reserved.
	stageDraft batchStage = "draft"
	// stageWaiting: locked, and the next dispatch pass will try to send it.
	// Nothing is wrong; it has simply not had its turn, or no printer is free.
	stageWaiting batchStage = "waiting"
	// stageBlocked: locked, and the last attempt to send it failed. The bed
	// still retries - see sendCooldown - so this is "held up", not "dead".
	stageBlocked batchStage = "blocked"
	// stageSlicing: BambuBuddy has the plate and is slicing it. Minutes, and
	// the single longest stretch where the old board said nothing at all.
	stageSlicing batchStage = "slicing"
	// stageQueued: sliced and sitting in the printer's own queue, waiting for
	// BambuBuddy's dispatcher to start it. Tensor is finished with it.
	stageQueued batchStage = "queued"
	// stagePrinting: on the bed, running.
	stagePrinting batchStage = "printing"
	// stageDone: printed, and its planks have gone to assembly.
	stageDone batchStage = "done"
)

// stageLabel is what the operator reads. Present tense for the stages that are
// actively moving, because the thing they tell you is that waiting is correct.
var stageLabel = map[batchStage]string{
	stageDraft:    "Collecting planks",
	stageWaiting:  "Waiting for a printer",
	stageBlocked:  "Held up",
	stageSlicing:  "Slicing…",
	stageQueued:   "Queued on the printer",
	stagePrinting: "Printing…",
	stageDone:     "Done",
}

// batchStageOf reads the stage off the same columns nextDispatchStep reads.
//
// The order is the point, and it mirrors nextDispatchStep's: status first for
// the two ends of the journey, then the markers in order of how far along they
// prove the bed is, then the error, then the default.
//
// Markers are checked BEFORE print_error deliberately. A marker is evidence of
// something currently happening; print_error is evidence of something that
// happened. A bed that failed, rested, and has since been picked up again
// carries both for as long as the new attempt runs, and "Slicing…" is the true
// answer there - the stale reason is still on the row for the detail line, but
// it is not what the bed is doing.
func batchStageOf(b gen.Batch, now time.Time) batchStage {
	switch {
	case b.Status == production.BatchCompleted:
		return stageDone
	case b.Status == production.BatchInProgress:
		return stagePrinting
	case b.Status == production.BatchPendingApproval:
		return stageDraft

	// Sliced, and handed over. queue_item_id is the last marker to appear and
	// the most specific, so it is read before the slice job that produced it.
	case b.QueueItemID != nil:
		return stageQueued
	// Uploaded and slicing. pipeline_run_id is the same evidence on the older
	// path, and a bed on that path is just as much "not idle" as one on this.
	case b.BambuSliceJobID != nil || b.PipelineRunID != nil:
		return stageSlicing

	// Held up, and resting before the next try. Only while the rest is actually
	// in force: a bed waiting hours on a spool keeps its reason but its error
	// ages out of the cooldown, and at that point it is being retried every
	// pass - which is waiting, not blocked. Saying "Held up" for a bed Tensor
	// tries every seven minutes would make the word meaningless on the one bed
	// where it matters.
	case b.PrintErrorAt.Valid && now.Sub(b.PrintErrorAt.Time) < sendCooldown:
		return stageBlocked

	default:
		return stageWaiting
	}
}
