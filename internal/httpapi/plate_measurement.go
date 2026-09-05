package httpapi

// Learning how long a plate actually takes to print.
//
// Every scheduling decision downstream is arithmetic on print time: when a
// machine comes free, and therefore which machine the next bed should be built
// for. Tensor used to get that number by slicing the plate itself - Bambu Studio
// under xvfb, minutes of CPU per bed - and write it back through
// SetBatchPlateSliceResult.
//
// It does not slice any more. BambuBuddy does, on the printer host, which is
// what let Tensor stop shipping a slicer. But nothing ever taught Tensor to ask
// for the answer, so every batch in the database carries no measured time at all
// (42 of 42 when this was written) and the scheduler has been projecting machine
// availability from nothing.
//
// BambuBuddy already has it. Its queue item carries print_time_seconds,
// filament_used_grams, the layer height and the model it was sliced for - filled
// in the moment the slice completes. This reads them back onto the batch through
// the same query the old slice worker used, so the rest of the system cannot
// tell which slicer produced the number.

import (
	"context"
	"math"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// PlateMeasurementOutcome is what one reconciliation pass learned.
type PlateMeasurementOutcome struct {
	// Awaiting is how many beds are queued without a measurement yet.
	Awaiting int
	// Measured is how many gained one this pass.
	Measured int
	// Pending is how many are queued but BambuBuddy has not sliced yet - not a
	// problem, just work in progress, and worth separating from beds whose
	// queue item has vanished.
	Pending int
	// Missing is how many point at a queue item BambuBuddy no longer has:
	// someone removed it, or the print finished and was archived.
	Missing int
}

// RecordPlateMeasurements copies BambuBuddy's sliced figures onto the beds that
// are waiting for them.
//
// Best-effort and idempotent: a bed with plate_sliced_at set is never
// reconsidered, so a pass costs one queue read regardless of how long the fleet
// has been running.
func (s *Server) RecordPlateMeasurements(ctx context.Context) PlateMeasurementOutcome {
	log := obs.FromContext(ctx)
	var out PlateMeasurementOutcome

	if s.bambu == nil || !s.bambu.Configured() {
		return out
	}
	waiting, err := s.store.Q.ListBatchesAwaitingPlateMeasurement(ctx)
	if err != nil {
		log.Warn("could not list beds awaiting a plate measurement", "error", err)
		return out
	}
	out.Awaiting = len(waiting)
	if len(waiting) == 0 {
		return out
	}

	queue, err := s.bambu.ListQueue(ctx)
	if err != nil {
		log.Warn("could not read BambuBuddy's queue for plate measurements", "error", err)
		return out
	}
	byID := make(map[int]bambubuddy.QueueItem, len(queue))
	for _, item := range queue {
		byID[item.ID] = item
	}

	for _, b := range waiting {
		if b.QueueItemID == nil {
			continue
		}
		item, ok := byID[int(*b.QueueItemID)]
		if !ok {
			out.Missing++
			continue
		}
		if item.PrintTimeSeconds <= 0 {
			// Queued but not sliced yet. The next pass picks it up.
			out.Pending++
			continue
		}
		if err := s.recordPlateMeasurement(ctx, b, item); err != nil {
			log.Warn("could not record a plate measurement",
				"batch", b.BatchNumber, "queue_item", item.ID, "error", err)
			continue
		}
		out.Measured++
		log.Info("plate measured by BambuBuddy",
			"batch", b.BatchNumber, "minutes", item.PrintTimeSeconds/60,
			"grams", item.FilamentUsedGrams, "sliced_for", item.SlicedForModel)
	}
	return out
}

// recordPlateMeasurement writes one queue item's figures onto its bed.
func (s *Server) recordPlateMeasurement(
	ctx context.Context, b gen.ListBatchesAwaitingPlateMeasurementRow, item bambubuddy.QueueItem,
) error {
	// Rounded to the minute, floored at one: the rest of the system stores print
	// time in minutes, and a plate that rounds to zero would read as a bed that
	// costs a machine nothing to run.
	total := int32(math.Round(float64(item.PrintTimeSeconds) / 60))
	if total < 1 {
		total = 1
	}
	params := gen.SetBatchPlateSliceResultParams{ID: b.ID, TotalPrintTimeMinutes: &total}

	// Per unit, from the same measured total, so the two can never disagree -
	// the same rule the old slice worker followed.
	if b.UnitsPerBed != nil && *b.UnitsPerBed > 0 {
		eff := float64(total) / float64(*b.UnitsPerBed)
		params.EffectiveTimePerUnitMinutes = &eff
	}
	if item.FilamentUsedGrams > 0 {
		grams := item.FilamentUsedGrams
		params.TotalFilamentGrams = &grams
	}
	// Support, purge and colour changes are deliberately left alone. BambuBuddy's
	// queue item does not break the plate down that way, and writing zeros would
	// replace "not measured" with "measured as none".
	_, err := s.store.Q.SetBatchPlateSliceResult(ctx, params)
	return err
}
