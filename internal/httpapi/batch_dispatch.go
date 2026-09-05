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

		switch {
		case b.Status == production.BatchPendingApproval:
			// A Draft that still has room is left alone. Approving it would
			// freeze a half-empty bed, and the whole point of leaving it a
			// Draft is that the next order in the same colour joins it instead
			// of opening a bed of its own. agedOut is the release valve: after
			// BATCH_MAX_WAIT_HOURS it prints as it is, so a lone plank in an
			// unpopular colour is not held for company that never arrives.
			if !s.readyToLock(ctx, b) {
				out.HeldOpen++
				continue
			}
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

		case b.QueueItemID != nil:
			// Already in BambuBuddy's queue. Sending again would put a second
			// copy of the same bed on a printer.

		default:
			resp, err := s.SendBatchToPrinter(ctx, b)
			if err != nil {
				out.Failed++
				log.Warn("could not send a batch to a printer", "batch", b.BatchNumber, "error", err)
				continue
			}
			out.Sent++
			// Queued false is not a failure - the plate is in the library and
			// BambuBuddy said why it is not moving. That reason is the useful
			// half of the line.
			log.Info("batch dispatched", "batch", b.BatchNumber,
				"queued", resp.Queued, "note", resp.Note)
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
