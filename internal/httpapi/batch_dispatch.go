package httpapi

// Moving batches toward a printer without anyone pressing a button.
//
// The pipeline already had every step - plan, approve, slice, send - but each
// one waited for a person, so a bed the planner built at 02:00 sat until
// somebody opened the page. This walks them, OLDEST ORDER FIRST, and takes
// whichever single step each batch is ready for.
//
// One step per batch per run, deliberately. Approving is a commitment - it
// reserves filament and stamps a machine - and sending is a multi-megabyte
// upload followed by a slice on the printer host. Taking both for the same bed
// in one pass would double the work a single run can stall on.

import (
	"context"
	"errors"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// DispatchOutcome is what one pass did, for the log and the caller's tests.
type DispatchOutcome struct {
	Considered int
	Approved   int
	Sent       int
	// HeldOpen counts Drafts left alone because they still have room. They are
	// not stuck: a bed under the cap is deliberately still absorbing work, and
	// locking one early would send a half-empty plate while the next order in
	// its colour opened a bed of its own.
	//
	// This field used to be "Waiting", meaning waiting on a plate slice, which
	// has had no meaning since BambuBuddy took over slicing.
	HeldOpen int
	// Failed counts batches whose step errored. The reason is recorded on the
	// batch itself (print_error), so this is a count rather than a list.
	Failed int
	// NoPrinter counts beds the fleet cannot take yet: the colour is loaded
	// nowhere, or every printer holding it is off, faulted or locked out.
	//
	// Not Failed. Nothing is broken, the reason is on the bed for the floor to
	// act on, and it goes the moment somebody loads a spool. Counted apart so a
	// shop with one unmapped colour does not read as a dispatcher throwing
	// errors every seven minutes.
	NoPrinter int
}

// dispatchStep is the one thing a bed is ready for this pass.
type dispatchStep int

const (
	// stepNone: already moving, or resolved. Costs no budget.
	stepNone dispatchStep = iota
	stepHoldOpen
	stepApprove
	stepSend
)

// nextDispatchStep decides what one bed needs, and nothing else.
//
// Pure, so the walk's rules can be asserted without a fleet, a database or
// BambuBuddy - which is why batch_dispatch.go had no test until now.
func nextDispatchStep(b gen.Batch, readyToLock bool) dispatchStep {
	switch {
	case b.Status == production.BatchPendingApproval && !readyToLock:
		return stepHoldOpen
	case b.Status == production.BatchPendingApproval:
		return stepApprove
	// Already on its way, by any of THREE markers rather than one.
	//
	// queue_item_id only appears once the slice finishes, which is minutes.
	// Between asking for a slice and that moment bambu_slice_job_id is the only
	// evidence the bed is moving, and pipeline_run_id is the same evidence on
	// the older path. Reading queue_item_id alone sent every mid-slice bed back
	// into the send branch, where the already-sent guard turned it into a
	// no-op - and each no-op spent a slot against the per-run cap. Five beds
	// slicing meant a pass that did nothing at all, and kept doing nothing
	// until they landed.
	case b.QueueItemID != nil || b.PipelineRunID != nil || b.BambuSliceJobID != nil:
		return stepNone
	// Belt and braces: ListBatchesToDispatch already excludes these, and the
	// rule that a failed plate is never retried unattended is stated in three
	// other files. One of them should be in the walk itself.
	case b.PrintOutcome != nil:
		return stepNone
	default:
		return stepSend
	}
}

// DispatchReadyBatches advances every batch one step, oldest order first.
//
// Best-effort per batch: one bed that cannot be approved (a job went on hold
// since it was planned) or cannot be sent (BambuBuddy refused the file) must not
// stop the beds behind it. Each failure is recorded on its own batch and the
// walk continues.
func (s *Server) DispatchReadyBatches(ctx context.Context) DispatchOutcome {
	log := obs.FromContext(ctx)
	var out DispatchOutcome

	if !s.cfg.BatchAutoDispatch {
		return out
	}

	// Checked once for the pass, not once per bed. The handler gets these from
	// filesReady and its own BambuBuddy check before it touches a batch; the
	// worker has neither, and without them every bed in the list would record
	// "BambuBuddy is not configured" as its print_error, every seven minutes,
	// for a fault that has nothing to do with any of them.
	if s.storage == nil || !s.bambu.Configured() {
		log.Info("batch dispatch skipped: object storage or BambuBuddy is not configured")
		return out
	}

	rows, err := s.store.Q.ListBatchesToDispatch(ctx)
	if err != nil {
		log.Warn("could not list batches to dispatch", "error", err)
		return out
	}

	max := s.cfg.BatchAutoDispatchMax
	if max <= 0 {
		max = defaultAutoDispatchMax
	}

	for _, b := range rows {
		if out.Approved+out.Sent >= max {
			// Not an error, and worth saying: the rest are simply next run's
			// work. Without the cap, turning this on against a backlog would
			// approve every bed at once and queue every plate slice behind it.
			log.Info("batch dispatch reached its per-run limit",
				"limit", max, "approved", out.Approved, "sent", out.Sent)
			break
		}
		out.Considered++

		// A Draft that still has room is left alone. Approving it would freeze
		// a half-empty bed, and the whole point of leaving it a Draft is that
		// the next order in the same colour joins it instead of opening a bed
		// of its own.
		readyToLock := b.Status == production.BatchPendingApproval && s.readyToLock(ctx, b)

		switch nextDispatchStep(b, readyToLock) {
		case stepNone:
			// Already moving, or resolved. Deliberately spends no budget: a
			// bed mid-slice is not work this pass can do, and counting it as
			// work is how a pass full of slicing beds did nothing at all.
			out.Considered--

		case stepHoldOpen:
			out.HeldOpen++

		case stepApprove:
			// Approving commits the bed: it reserves filament, stamps a machine
			// and enqueues the plate slice. Attributed to systemActor because no
			// person is behind it - see production_events.go.
			if _, err := s.ApproveBatchFor(ctx, b.ID, nil, systemActor); err != nil {
				out.Failed++
				log.Warn("could not auto-approve a batch", "batch", b.BatchNumber, "error", err)
				continue
			}
			out.Approved++
			log.Info("batch auto-approved, plate slice queued", "batch", b.BatchNumber)

		case stepSend:
			s.autoSendOneBatch(ctx, b, &out)
		}
	}

	if out.Approved > 0 || out.Sent > 0 || out.Failed > 0 {
		log.Info("batch dispatch pass complete",
			"considered", out.Considered, "approved", out.Approved, "sent", out.Sent,
			"held_open", out.HeldOpen, "failed", out.Failed)
	}
	return out
}

// defaultAutoDispatchMax is how many batches one pass will advance when
// BATCH_AUTO_DISPATCH_MAX is unset.
//
// Five, because each approval starts a plate slice and each send is a
// multi-megabyte upload - both slow, and both competing with the slicer that is
// already running. A backlog drains over several passes instead of arriving as
// one stampede.
const defaultAutoDispatchMax = 5

// autoSendOneBatch chooses a printer for one bed and sends it there.
//
// The same two calls the Queue button makes - chooseTargetFor, then
// sendBatchToMachine - so an automatic send and a pressed one cannot decide
// differently. What changes is only what happens to the answer: a person reads
// a 409, and this writes it on the bed for whoever walks past next.
func (s *Server) autoSendOneBatch(ctx context.Context, b gen.Batch, out *DispatchOutcome) {
	log := obs.FromContext(ctx)

	target, err := s.chooseTargetFor(ctx, b)
	if err != nil {
		// The fleet cannot take it yet. Expected, recorded, and tried again
		// next pass: it comes right the moment somebody loads a spool, with
		// nobody having to press anything.
		if errors.Is(err, errNoPrinter) {
			out.NoPrinter++
			s.recordPrintErrorOnce(ctx, b.ID, reasonOf(err))
			log.Info("no printer can take a bed yet",
				"batch", b.BatchNumber, "note", reasonOf(err))
			return
		}
		out.Failed++
		s.recordPrintErrorOnce(ctx, b.ID, reasonOf(err))
		log.Warn("could not choose a printer for a bed", "batch", b.BatchNumber, "error", err)
		return
	}

	resp, err := s.sendBatchToMachine(ctx, b, target.Machine, target.SlotTrays,
		systemActor, automaticSend)
	if err != nil {
		out.Failed++
		s.recordPrintErrorOnce(ctx, b.ID, reasonOf(err))
		log.Warn("could not send a batch to a printer", "batch", b.BatchNumber, "error", err)
		return
	}
	if !resp.Queued {
		// A nil error that is not a send: the bed was already on its way, or a
		// slice is running that nothing will queue. Counted as failed rather
		// than sent, because a worker that reads err == nil as success leaves
		// the second case parked for ever.
		out.Failed++
		log.Warn("a bed was not sent", "batch", b.BatchNumber, "note", resp.Note)
		return
	}

	out.Sent++
	// The reason THIS printer won, which the response carries to an operator
	// and nothing carries to anyone when the dispatcher chose. Logged so the
	// answer to "why that one?" exists somewhere.
	log.Info("bed auto-sent", "batch", b.BatchNumber,
		"machine", target.Machine.Name, "why", target.Reason, "note", resp.Note)
}

// reasonOf is the operator-facing half of a failure, never its cause.
//
// statusError.Error() appends the wrapped cause - "Could not read the batch's
// jobs: dial tcp ..." - which is right for a log line and wrong for a red note
// on the Batches page.
func reasonOf(err error) string {
	var se *statusError
	if errors.As(err, &se) {
		return se.msg
	}
	return "Tensor could not send this bed to a printer."
}
