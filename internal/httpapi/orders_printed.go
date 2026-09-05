package httpapi

// Marking a list of orders printed, and putting their beds back in order.
//
// The floor works from a paper list: the plates that came off the printers, by
// order number. Reconciling that by hand is dozens of clicks - complete each
// job, then work out what is left on each bed - and the second half is the part
// that goes wrong, because a bed that loses two of its four planks is not
// finished and is not empty either. It is a bed with two free places that
// nothing will ever fill, because a locked bed has left the planning pool.
//
// So this does both halves. Jobs for the printed orders are completed; a bed
// whose whole contents printed is marked Done; and a bed that printed only
// partly is unlocked back to a Draft, which is what lets the planner refill it
// to four from the queue. Filament reserved at approval is credited back when a
// bed is unlocked, so the re-approval that follows does not charge for the same
// bed twice.

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// PrintedOutcome is what one reconciliation did.
type PrintedOutcome struct {
	// Matched is the order numbers found in Tensor; Missing is the rest, named
	// rather than counted - a number that matched nothing is usually a
	// misreading of somebody's handwriting, and a count would hide which.
	Matched []string
	Missing []string

	JobsCompleted    int
	BatchesCompleted []string
	BatchesReopened  []string
}

// orderNumberKey reduces an order number to the digits people actually write.
//
// Tensor stores "T3DPS-114762"; the floor's list says "114762". The TRAILING run
// of digits is the key, not every digit in the string - the store prefix itself
// contains a "3", so keeping all of them turned T3DPS-114762 into 3114762 and
// matched nothing at all.
func orderNumberKey(s string) string {
	end := len(s)
	for end > 0 && (s[end-1] < '0' || s[end-1] > '9') {
		end--
	}
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	return s[start:end]
}

// dedupeKeys returns the distinct order numbers in the order given.
//
// The floor's list repeats: an order with planks in two colours is written under
// each machine that ran one. Left in, the repeats double-count jobs in the
// preview and make a bed look more printed than it is.
func dedupeKeys(orderNumbers []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(orderNumbers))
	for _, n := range orderNumbers {
		key := orderNumberKey(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out
}

// MarkOrdersPrinted completes the jobs for the given order numbers and settles
// the beds they were on.
//
// Idempotent: completing an already-completed job touches nothing, a bed already
// Done stays Done, and a bed already reopened is simply found under the cap
// again. Running it twice over the same list is safe.
func (s *Server) MarkOrdersPrinted(ctx context.Context, orderNumbers []string) (PrintedOutcome, error) {
	log := obs.FromContext(ctx)
	out := PrintedOutcome{}

	orderNumbers = dedupeKeys(orderNumbers)
	orders, err := s.store.Q.ListOrders(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("list orders: %w", err)
	}
	byKey := make(map[string]gen.Order, len(orders))
	for _, o := range orders {
		byKey[orderNumberKey(o.OrderNumber)] = o
	}

	matched := make([]gen.Order, 0, len(orderNumbers))
	for _, raw := range orderNumbers {
		order, ok := byKey[orderNumberKey(raw)]
		if !ok {
			out.Missing = append(out.Missing, raw)
			continue
		}
		matched = append(matched, order)
		out.Matched = append(out.Matched, order.OrderNumber)
	}

	// Beds first, and that ordering is the whole trick.
	//
	// Completing a bed completes its jobs while they stay ON it, so the finished
	// bed still knows which orders and colours it held - which is what the
	// Completed list shows. Completing the jobs first takes them off the bed
	// (that is how a partly printed bed frees space), and a bed reached
	// afterwards is empty: no orders, no colours, nothing to read.
	printedOnBed := s.printedJobsByBed(ctx, matched)
	for batchID, printed := range printedOnBed {
		s.completeIfWhollyPrinted(ctx, batchID, printed, &out)
	}

	// Then the sweep, for everything the first pass did not finish: jobs on a
	// bed that printed only partly, and jobs on no bed at all. Jobs completed
	// above are untouched - this only moves jobs still queued or in production.
	touched := map[uuid.UUID]bool{}
	for _, order := range matched {
		rows, err := s.store.Q.CompleteJobsForFulfilledOrder(ctx, ptr(order.ID))
		if err != nil {
			// One order failing must not abandon the rest of the list: they are
			// independent, and stopping here would leave half the floor's work
			// unrecorded with no way to tell which half.
			log.Warn("could not complete a printed order's jobs",
				"order", order.OrderNumber, "error", err)
			continue
		}
		out.JobsCompleted += len(rows)
		for _, r := range rows {
			if r.FreedBatchID != nil {
				touched[*r.FreedBatchID] = true
			}
		}
	}

	for id := range touched {
		s.settlePrintedBatch(ctx, id, &out)
	}

	// The reopened beds have places to fill and the freed jobs are back in the
	// pool. One replan, after the whole list, rather than one per order.
	if len(out.BatchesReopened) > 0 || len(out.BatchesCompleted) > 0 {
		s.triggerBatchPlan(ctx)
	}
	log.Info("printed orders reconciled",
		"matched", len(out.Matched), "missing", len(out.Missing),
		"jobs_completed", out.JobsCompleted,
		"batches_completed", len(out.BatchesCompleted),
		"batches_reopened", len(out.BatchesReopened))
	return out, nil
}

// printedJobsByBed counts, per bed, how many of its jobs belong to the printed
// orders. Only jobs still in flight count: one already completed is not evidence
// about this run.
func (s *Server) printedJobsByBed(ctx context.Context, orders []gen.Order) map[uuid.UUID]int {
	log := obs.FromContext(ctx)
	perBed := map[uuid.UUID]int{}
	for _, order := range orders {
		jobs, err := s.store.Q.ListProductionJobs(ctx, gen.ListProductionJobsParams{
			OrderID: ptr(order.ID),
		})
		if err != nil {
			log.Warn("could not read a printed order's jobs", "order", order.OrderNumber, "error", err)
			continue
		}
		for _, j := range jobs {
			if j.BatchID == nil {
				continue
			}
			if j.Status != production.StatusQueued && j.Status != production.StatusInProduction {
				continue
			}
			perBed[*j.BatchID]++
		}
	}
	return perBed
}

// completeIfWhollyPrinted marks a bed Done when every plank on it printed.
//
// A bed with planks left is not touched here - the sweep that follows completes
// its printed jobs and frees them, and settlePrintedBatch reopens what remains.
func (s *Server) completeIfWhollyPrinted(ctx context.Context, batchID uuid.UUID, printed int, out *PrintedOutcome) {
	log := obs.FromContext(ctx)

	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		log.Warn("could not load a bed whose orders printed", "batch", batchID, "error", err)
		return
	}
	// A bed that is printing or already Done is a record of what physically
	// happened, and is left as it stands.
	if batch.Status != production.BatchPendingApproval && batch.Status != production.BatchOpen {
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		log.Warn("could not read a bed whose orders printed", "batch", batchID, "error", err)
		return
	}
	if len(jobs) == 0 || printed < len(jobs) {
		return
	}
	if _, err := s.SetBatchStatus(ctx, batchID, production.BatchCompleted); err != nil {
		log.Warn("could not mark a fully printed bed done", "batch", batch.BatchNumber, "error", err)
		return
	}
	out.JobsCompleted += len(jobs)
	out.BatchesCompleted = append(out.BatchesCompleted, batch.BatchNumber)
	log.Info("bed fully printed, marked done", "batch", batch.BatchNumber, "planks", len(jobs))
}

// settlePrintedBatch decides what a bed that lost jobs has become.
//
// Empty means every plank on it printed, so the bed is Done. Anything left
// means it printed only partly, and the bed goes back to being a Draft with
// room - the only state from which the planner will refill it to four.
func (s *Server) settlePrintedBatch(ctx context.Context, batchID uuid.UUID, out *PrintedOutcome) {
	log := obs.FromContext(ctx)

	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		log.Warn("could not load a bed after its orders printed", "batch", batchID, "error", err)
		return
	}
	// A bed that is printing or already Done is a record of what physically
	// happened. Its jobs were completed above but its membership stands - see
	// CompleteJobsForFulfilledOrder.
	if batch.Status != production.BatchPendingApproval && batch.Status != production.BatchOpen {
		return
	}

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		log.Warn("could not read a bed after its orders printed", "batch", batchID, "error", err)
		return
	}
	if len(jobs) == 0 {
		// Backstop. completeIfWhollyPrinted normally gets here first and marks
		// the bed Done with its jobs still on it; a bed that reaches this point
		// empty lost its last plank some other way, and an empty Draft is not a
		// proposal anybody can act on.
		if _, err := s.SetBatchStatus(ctx, batchID, production.BatchCompleted); err != nil {
			log.Warn("could not mark an emptied bed done", "batch", batch.BatchNumber, "error", err)
			return
		}
		out.BatchesCompleted = append(out.BatchesCompleted, batch.BatchNumber)
		log.Info("bed emptied by printed orders, marked done", "batch", batch.BatchNumber)
		return
	}

	// Still has planks on it. Nothing to do if it is already a Draft - it is in
	// the planning pool and the next run tops it back up.
	if batch.Status != production.BatchOpen {
		s.rebuildBatchAfterRemoval(ctx, batchID)
		return
	}
	if err := s.unlockBatch(ctx, batch, jobs); err != nil {
		log.Warn("could not unlock a partly printed bed", "batch", batch.BatchNumber, "error", err)
		return
	}
	s.rebuildBatchAfterRemoval(ctx, batchID)
	out.BatchesReopened = append(out.BatchesReopened, batch.BatchNumber)
	log.Info("bed partly printed, reopened to be refilled",
		"batch", batch.BatchNumber, "planks_left", len(jobs))
}

// unlockBatch returns a locked bed to being a Draft and credits back the
// filament its approval reserved.
//
// The credit is the whole bed's reservation, not the remaining jobs': approval
// decremented for everything on the bed, and the re-approval that follows will
// decrement again for whatever the bed ends up holding. Crediting only part
// would leave the difference reserved against a bed that no longer exists.
func (s *Server) unlockBatch(ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob) error {
	return s.store.InTx(ctx, func(q *gen.Queries) error {
		if batch.FilamentReserved {
			if err := s.adjustFilamentByColour(ctx, q, filamentSplitForJobs(jobs), +1); err != nil {
				return err
			}
		}
		_, err := q.ReopenBatchForReplanning(ctx, batch.ID)
		return err
	})
}

// PreviewOrdersPrinted reports what MarkOrdersPrinted would do, writing nothing.
//
// Worth having for a list transcribed off a photograph of somebody's
// handwriting: a misread digit either matches no order (visible here) or matches
// the wrong one (visible here too, as a bed nobody expected). Reading that back
// before committing costs nothing; undoing a wrongly completed job costs a
// reprint.
func (s *Server) PreviewOrdersPrinted(ctx context.Context, orderNumbers []string) (string, error) {
	orderNumbers = dedupeKeys(orderNumbers)
	orders, err := s.store.Q.ListOrders(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("list orders: %w", err)
	}
	byKey := make(map[string]gen.Order, len(orders))
	for _, o := range orders {
		byKey[orderNumberKey(o.OrderNumber)] = o
	}

	var (
		b       strings.Builder
		missing []string
		jobs    int
		// Per bed: how many of its jobs are on the list.
		onList = map[uuid.UUID]int{}
	)
	for _, raw := range orderNumbers {
		order, ok := byKey[orderNumberKey(raw)]
		if !ok {
			missing = append(missing, raw)
			continue
		}
		rows, err := s.store.Q.ListProductionJobs(ctx, gen.ListProductionJobsParams{
			OrderID: ptr(order.ID),
		})
		if err != nil {
			return "", fmt.Errorf("read jobs for %s: %w", order.OrderNumber, err)
		}
		for _, j := range rows {
			if j.Status != production.StatusQueued && j.Status != production.StatusInProduction {
				continue
			}
			jobs++
			if j.BatchID != nil {
				onList[*j.BatchID]++
			}
		}
	}

	fmt.Fprintf(&b, "orders on the list: %d\norders found in Tensor: %d\njobs that would be completed: %d\n",
		len(orderNumbers), len(orderNumbers)-len(missing), jobs)
	if len(missing) > 0 {
		fmt.Fprintf(&b, "NOT FOUND in Tensor (%d): %v\n", len(missing), missing)
	}

	fmt.Fprintf(&b, "\nbeds affected: %d\n", len(onList))
	for id, n := range onList {
		batch, err := s.store.Q.GetBatchByID(ctx, id)
		if err != nil {
			continue
		}
		held, err := s.store.Q.ListJobsForBatch(ctx, &id)
		if err != nil {
			continue
		}
		verdict := "reopened to refill"
		if n >= len(held) {
			verdict = "marked done"
		}
		if batch.Status != production.BatchPendingApproval && batch.Status != production.BatchOpen {
			verdict = "left alone (already " + batch.Status + ")"
		}
		fmt.Fprintf(&b, "  %s  %s  %d of %d printed -> %s\n",
			batch.BatchNumber, batch.Status, n, len(held), verdict)
	}
	return b.String(), nil
}
