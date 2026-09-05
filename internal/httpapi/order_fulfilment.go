package httpapi

// What happens to production work when Shopify says an order has shipped.
//
// Fulfilment is the end of the story: the parcel has left, so nothing about that
// order is still work for the print floor. Without this, its jobs sat in the
// queue for ever and - worse - kept occupying bed space, so a live order waited
// behind a plank that had already been posted.
//
// Reconciled on every import rather than only on a change, because the import is
// the only moment Tensor learns anything: orders are pulled, not pushed (see
// shopify_import.go), so "it just became fulfilled" is not a distinguishable
// event. Doing it unconditionally is safe because the work is idempotent - the
// query only touches jobs still in flight, so a second run over the same order
// changes nothing.

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// fulfilledStatus is Shopify's own word for "it has shipped".
//
// Matched exactly rather than as a substring: "partially_fulfilled" is NOT this.
// Part of an order having shipped says nothing about the plank still on a bed,
// and treating it as done would cancel work somebody is still waiting for.
const fulfilledStatus = "fulfilled"

// reconcileFulfilledOrder completes an order's outstanding jobs and frees them
// from any bed still open to change.
//
// Best-effort: an import that succeeded must not be failed by the tidying that
// follows it. A failure here leaves the jobs where they were, and the next sync
// of the same order tries again.
// Returns how many jobs it closed, so a caller sweeping many orders can report
// what actually changed without counting them first.
func (s *Server) reconcileFulfilledOrder(ctx context.Context, order gen.Order) int {
	if order.FulfillmentStatus == nil ||
		!strings.EqualFold(strings.TrimSpace(*order.FulfillmentStatus), fulfilledStatus) {
		return 0
	}
	log := obs.FromContext(ctx)

	rows, err := s.store.Q.CompleteJobsForFulfilledOrder(ctx, ptr(order.ID))
	if err != nil {
		log.Warn("could not close out a fulfilled order's jobs",
			"order", order.OrderNumber, "error", err)
		return 0
	}
	if len(rows) == 0 {
		return 0
	}

	// Beds that lost a job need their figures redone: units, filament and the
	// time estimate the machine scheduler ranks on all described a plate that no
	// longer exists. A bed emptied completely is deleted rather than left as an
	// empty proposal nobody can act on.
	freed := map[uuid.UUID]bool{}
	for _, r := range rows {
		if r.FreedBatchID != nil {
			freed[*r.FreedBatchID] = true
		}
	}
	for id := range freed {
		s.rebuildBatchAfterRemoval(ctx, id)
	}

	log.Info("fulfilled order closed out of production",
		"order", order.OrderNumber, "jobs_completed", len(rows), "batches_touched", len(freed))

	// The freed bed space is worth re-planning for: the jobs behind this order
	// can now move up. Debounced like every other trigger, so a sync of two
	// hundred orders produces one replan rather than two hundred.
	if len(freed) > 0 {
		s.triggerBatchPlan(ctx)
	}
	return len(rows)
}

// rebuildBatchAfterRemoval settles a bed that has lost a plank.
//
// The plank shipped, so it must leave the plate as well as the bed. Recomputing
// the numbers is not enough: the merged 3MF still holds its geometry, and for a
// bed already handed to BambuBuddy that plate is queued in front of a printer.
// Left alone, the machine prints the plank that is already with the customer and
// the three beside it come out on a plate nobody expected.
//
// So this does what a hand edit does (see batch_edit.go), by the same steps and
// for the same reasons:
//
//  1. take the plate back out of BambuBuddy's queue
//  2. drop everything that described the old contents - merged plate, preview,
//     slice, queue and pipeline ids
//  3. rebuild the plate from what is left, and re-reserve for that
//
// Filament is deliberately released and re-reserved for the REMAINING planks
// only. The shipped one's filament was genuinely consumed, so leaving it debited
// is the honest position - which is what happens when the credit covers only
// what is still on the bed.
func (s *Server) rebuildBatchAfterRemoval(ctx context.Context, batchID uuid.UUID) {
	log := obs.FromContext(ctx)

	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		log.Warn("could not load a batch after removing a fulfilled job", "batch", batchID, "error", err)
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		log.Warn("could not read a batch after removing a fulfilled job", "batch", batchID, "error", err)
		return
	}

	// Whatever is left of this bed, its queued plate is now wrong.
	if err := s.withdrawFromPrinterQueue(ctx, batch); err != nil {
		// Not fatal. The bed is corrected either way; a plate left in
		// BambuBuddy's queue is visible there and can be removed by hand, and
		// refusing to fix Tensor because BambuBuddy is unreachable helps nobody.
		log.Warn("could not withdraw a bed's plate after a fulfilled order left it",
			"batch", batch.BatchNumber, "error", err)
	}

	if len(jobs) == 0 {
		s.closeEmptiedBatch(ctx, batch)
		return
	}

	// Everything that described the old contents goes first, so a failure below
	// leaves a bed with no plate rather than a bed with the wrong one.
	if _, err := s.store.Q.ClearBatchPlateForEdit(ctx, batchID); err != nil {
		log.Warn("could not clear a batch's old plate", "batch", batch.BatchNumber, "error", err)
	}
	if batch.FilamentReserved {
		if err := s.adjustFilamentByColour(ctx, s.store.Q, filamentSplitForJobs(jobs), +1); err != nil {
			log.Warn("could not release a batch's filament", "batch", batch.BatchNumber, "error", err)
		}
	}

	units := int32(unitsOf(jobs))
	filament := sumFilament(jobs)
	total, effective := batchTimeFromJobs(jobs)
	params := gen.UpdateBatchDerivedMetricsParams{
		ID: batchID, UnitsPerBed: &units, TotalFilamentGrams: &filament,
		TotalPrintTimeMinutes: total, EffectiveTimePerUnitMinutes: effective,
	}

	// The plate itself. Rebuilt here rather than left to previewBatch's
	// on-demand build, because a LOCKED bed has no next approval to rebuild it
	// at - the stale merged file would be exactly what gets sent.
	if plate, ok := s.replatedBatch(ctx, batch, jobs); ok {
		params.PreviewFileID = &plate.fileID
		params.UnitsPerBed = int32ptr(plate.unitsPerBed)
		params.BedUtilizationPercent = &plate.utilisation
	}

	if _, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, params); err != nil {
		log.Warn("could not recompute a batch after removing a fulfilled job",
			"batch", batch.BatchNumber, "error", err)
		return
	}
	if batch.FilamentReserved {
		if err := s.adjustFilamentByColour(ctx, s.store.Q, filamentSplitForJobs(jobs), -1); err != nil {
			log.Warn("could not reserve filament for the rebuilt batch",
				"batch", batch.BatchNumber, "error", err)
		}
	}

	// A locked bed has already been approved, so nothing else will send its
	// corrected plate. This is what does.
	if batch.Status == production.BatchOpen {
		s.triggerDispatch(ctx)
	}
	log.Info("bed re-plated after a fulfilled order left it",
		"batch", batch.BatchNumber, "planks_left", len(jobs))
}

// replatedPlate is a rebuilt plate and the figures that came with it.
type replatedPlate struct {
	fileID      uuid.UUID
	unitsPerBed int
	utilisation float64
}

// replatedBatch rebuilds a bed's merged plate from the jobs still on it.
//
// Best-effort: without object storage there is no plate to build, and a bed with
// its old plate cleared is still better than one advertising a plate that holds
// a plank already posted to a customer.
func (s *Server) replatedBatch(
	ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob,
) (replatedPlate, bool) {
	log := obs.FromContext(ctx)
	if s.storage == nil {
		log.Warn("no object storage, so a bed's plate was cleared and not rebuilt",
			"batch", batch.BatchNumber)
		return replatedPlate{}, false
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		log.Warn("could not rebuild a bed's plate", "batch", batch.BatchNumber, "reason", herr.msg)
		return replatedPlate{}, false
	}
	fileID, err := s.storePlateAs(ctx, batch.ID, "preview", plate, systemActor)
	if err != nil {
		log.Warn("could not store a rebuilt plate", "batch", batch.BatchNumber, "error", err)
		return replatedPlate{}, false
	}
	return replatedPlate{fileID: fileID, unitsPerBed: plate.unitsPerBed, utilisation: plate.utilisation}, true
}

// closeEmptiedBatch settles a bed whose every plank has shipped.
//
// A Draft is deleted - it was a proposal, and an empty proposal is not something
// anybody can act on. A LOCKED bed is marked Done instead: it was committed, it
// has a plate and a machine and a number people have quoted, and deleting that
// would make a bed somebody may have watched simply vanish.
func (s *Server) closeEmptiedBatch(ctx context.Context, batch gen.Batch) {
	log := obs.FromContext(ctx)
	if batch.Status == production.BatchPendingApproval {
		if err := s.store.Q.DeleteDraftBatches(ctx, []uuid.UUID{batch.ID}); err != nil {
			log.Warn("could not delete an emptied draft", "batch", batch.BatchNumber, "error", err)
		}
		return
	}
	if batch.Status == production.BatchCompleted {
		return
	}
	if _, err := s.SetBatchStatus(ctx, batch.ID, production.BatchCompleted); err != nil {
		log.Warn("could not finish an emptied bed", "batch", batch.BatchNumber, "error", err)
		return
	}
	log.Info("every plank on this bed has shipped, marking it done", "batch", batch.BatchNumber)
}

// CloseOutFulfilledOrders reconciles every order Shopify already reports as
// fulfilled.
//
// The catch-up for orders fulfilled before this existed, and the backstop for
// any the per-order path missed. Returns how many orders had work closed out.
func (s *Server) CloseOutFulfilledOrders(ctx context.Context) int {
	log := obs.FromContext(ctx)
	orders, err := s.store.Q.ListOrders(ctx, nil)
	if err != nil {
		log.Warn("could not list orders to close out fulfilled work", "error", err)
		return 0
	}
	closed := 0
	for _, o := range orders {
		if o.FulfillmentStatus == nil ||
			!strings.EqualFold(strings.TrimSpace(*o.FulfillmentStatus), fulfilledStatus) {
			continue
		}
		if s.reconcileFulfilledOrder(ctx, o) > 0 {
			closed++
		}
	}
	return closed
}
