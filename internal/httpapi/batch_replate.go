package httpapi

// Rebuilding a bed's plate from the models currently attached to its jobs.
//
// A merged plate is a snapshot: it is built once from whatever STLs the jobs
// held at the time and then stored as a file. Re-rendering the jobs does not
// touch it, so after a template, font or colour fix every bed still advertises
// - and would still print - the old geometry. rebuildBatchAfterRemoval already
// does this for one bed when a job leaves it; this is the same act for a bed
// whose jobs have not changed but whose MODELS have.
//
// It exists as its own entry point because the two triggers are different: that
// one is driven by an order shipping, this one by somebody deciding the models
// on disk are now correct.

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// RebuildBatchPlate re-merges one bed's plate from its jobs' current models.
//
// The bed's composition is left alone - same jobs, same machine, same number.
// Only the merged file and the figures derived from it are replaced. A bed
// whose jobs have no models is skipped rather than emptied: that is a job
// problem to fix, not a reason to strip a bed of its plate.
func (s *Server) RebuildBatchPlate(ctx context.Context, batchID uuid.UUID) error {
	log := obs.FromContext(ctx)

	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		return fmt.Errorf("load batch: %w", err)
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		return fmt.Errorf("read the bed's jobs: %w", err)
	}
	if len(jobs) == 0 {
		return fmt.Errorf("%s holds no jobs", batch.BatchNumber)
	}

	// The plate about to be replaced may already be queued at a printer. Taking
	// it back out first is the same ordering rebuildBatchAfterRemoval uses, and
	// for the same reason: a machine must never be left holding a plate Tensor
	// has since decided is wrong.
	if err := s.withdrawFromPrinterQueue(ctx, batch); err != nil {
		log.Warn("could not withdraw a bed's plate before re-plating",
			"batch", batch.BatchNumber, "error", err)
	}
	if _, err := s.store.Q.ClearBatchPlateForEdit(ctx, batchID); err != nil {
		return fmt.Errorf("clear the old plate: %w", err)
	}

	plate, ok := s.replatedBatch(ctx, batch, jobs)
	if !ok {
		return fmt.Errorf("%s: could not rebuild the plate", batch.BatchNumber)
	}

	units := int32(unitsOf(jobs))
	filament := sumFilament(jobs)
	total, effective := batchTimeFromJobs(jobs)
	params := gen.UpdateBatchDerivedMetricsParams{
		ID: batchID, UnitsPerBed: &units, TotalFilamentGrams: &filament,
		TotalPrintTimeMinutes: total, EffectiveTimePerUnitMinutes: effective,
		PreviewFileID:         &plate.fileID,
		BedUtilizationPercent: &plate.utilisation,
	}
	params.UnitsPerBed = int32ptr(plate.unitsPerBed)
	if _, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, params); err != nil {
		return fmt.Errorf("record the rebuilt plate: %w", err)
	}

	// A locked bed has already been approved, so nothing downstream will send
	// its corrected plate on its own.
	if batch.Status == production.BatchOpen {
		s.triggerDispatch(ctx)
	}
	log.Info("bed re-plated from its jobs' current models",
		"batch", batch.BatchNumber, "planks", len(jobs))
	return nil
}

// PlateableBatches are the beds a re-plate would touch: everything still
// holding jobs that has not been finished.
//
// Completed beds are excluded deliberately - their plate is the record of what
// was actually printed, and rewriting it would be falsifying history.
func (s *Server) PlateableBatches(ctx context.Context) ([]gen.Batch, error) {
	all, err := s.store.Q.ListBatches(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Batch, 0, len(all))
	for _, b := range all {
		if b.Status == production.BatchCompleted {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}
