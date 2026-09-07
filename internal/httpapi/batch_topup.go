package httpapi

// Putting expedited work onto beds that already exist.
//
// A locked bed is invisible to the planner: ListReplannableJobs returns
// unbatched jobs and Draft members only, so once a bed locks nothing can ever
// fill a place it has spare - whether it locked under-full or lost a plank to a
// fulfilled order. A priority order therefore waited for a whole new bed to
// form and fill, behind the bed that was about to print anyway.
//
// Draft beds need none of this. The planner dissolves and rebuilds them every
// run from a pool sorted priority-first (sortPriorityFirst), so an expedited
// plank already joins the first open bed of its colour and standard work fills
// the rest of that plate. This is only about the beds the planner can no longer
// see.

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxTopUpBedsPerRun caps how many beds one pass opens.
//
// Each one is a BambuBuddy round-trip, a mesh merge and a multi-megabyte
// upload, and the pass runs while the planner's mutex is held. Same reasoning
// as defaultAutoDispatchMax: a backlog drains over several runs rather than
// arriving as one stampede.
const maxTopUpBedsPerRun = 5

// TopUpOutcome is what one pass did.
type TopUpOutcome struct {
	BedsConsidered  int
	BedsFilled      int
	PriorityPlaced  int
	StandardPlaced  int
	SkippedPrinting int
	Failed          int
}

// TopUpLockedBedsWithPriority puts unbatched priority work onto locked beds
// that still have room, then fills what is left of those beds with standard
// work of the same configuration.
//
// Best-effort by design: it returns no error and never blocks planning. A
// BambuBuddy outage must not stop new beds being formed - that is the
// fall-through this feature relies on when it cannot place anything.
func (s *Server) TopUpLockedBedsWithPriority(ctx context.Context) TopUpOutcome {
	log := obs.FromContext(ctx)
	var out TopUpOutcome

	// Every edit ends in a rebuilt plate, so with no object storage there is
	// nothing this pass can legitimately do. Checked once, before any bed is
	// opened: beginBatchEdit withdraws the plate and credits the filament back,
	// and doing that for an edit that cannot finish strands the bed.
	if s.storage == nil {
		return out
	}

	cap := s.bedUnitCap()
	beds, err := s.store.Q.ListLockedBedsWithRoom(ctx, int32(cap))
	if err != nil {
		log.Warn("could not list locked beds with room", "error", err)
		return out
	}
	if len(beds) == 0 {
		return out
	}

	pool, err := s.store.Q.ListUnbatchedJobsByUrgency(ctx)
	if err != nil {
		log.Warn("could not list unbatched jobs for a top-up", "error", err)
		return out
	}
	if !anyPriority(pool) {
		// Nothing expedited is waiting, so no locked bed is worth opening.
		return out
	}

	taken := map[uuid.UUID]bool{}
	for _, bed := range beds {
		if out.BedsFilled >= maxTopUpBedsPerRun {
			log.Info("priority top-up reached its per-run limit",
				"limit", maxTopUpBedsPerRun, "beds_filled", out.BedsFilled)
			break
		}
		out.BedsConsidered++

		placed, err := s.fillLockedBed(ctx, bed, cap, pool, taken)
		switch {
		case errors.Is(err, errBedIsPrinting):
			out.SkippedPrinting++
		case err != nil:
			out.Failed++
			log.Warn("could not top up a locked bed", "batch", bed.BatchNumber, "error", err)
		case placed.priority > 0:
			out.BedsFilled++
			out.PriorityPlaced += placed.priority
			out.StandardPlaced += placed.standard
			log.Info("expedited work placed on a locked bed",
				"batch", bed.BatchNumber, "priority", placed.priority, "standard", placed.standard)
		}
	}
	return out
}

// errBedIsPrinting distinguishes "this plate is on a machine right now", which
// is a normal skip, from a genuine failure.
var errBedIsPrinting = errors.New("bed is printing")

// placement counts what one bed took.
type placement struct{ priority, standard int }

// fillLockedBed tops up one bed, or reports why it did not.
func (s *Server) fillLockedBed(
	ctx context.Context, bed gen.ListLockedBedsWithRoomRow, cap int,
	pool []gen.ProductionJob, taken map[uuid.UUID]bool,
) (placement, error) {
	batch, err := s.store.Q.GetBatchByID(ctx, bed.ID)
	if err != nil {
		return placement{}, err
	}
	existing, err := s.store.Q.ListJobsForBatch(ctx, &bed.ID)
	if err != nil {
		return placement{}, err
	}
	if len(existing) == 0 {
		return placement{}, nil
	}

	room := cap - unitsOf(existing)
	chosen, priorityCount := topUpCandidates(compatibilityKeyOf(existing[0]), room, pool, taken)
	if len(chosen) == 0 {
		return placement{}, nil
	}

	// From here the bed is being changed. beginBatchEdit takes the plate back
	// out of BambuBuddy's queue and credits its filament, so everything after
	// it has to run even on failure - see the rebuild below.
	if err := s.beginBatchEdit(ctx, batch); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Status() == http.StatusConflict {
			return placement{}, errBedIsPrinting
		}
		return placement{}, err
	}

	ids := make([]uuid.UUID, 0, len(chosen))
	for _, j := range chosen {
		ids = append(ids, j.ID)
	}
	rows, assignErr := s.assignWithinCap(ctx, bed.ID, ids, cap)

	// Begun is begun. The plate is out of the queue and the filament is
	// credited back, so the bed MUST be re-plated whatever happened above -
	// otherwise it is left under-reserved with a queue_item_id the dispatcher
	// reads as "already sent", and nothing ever retries it.
	if err := s.rebuildAfterTopUp(ctx, batch); err != nil {
		return placement{}, err
	}
	if assignErr != nil {
		return placement{}, assignErr
	}
	if rows == 0 {
		// Someone claimed the jobs between the read and the write.
		return placement{}, nil
	}
	for _, j := range chosen {
		taken[j.ID] = true
	}
	return placement{priority: priorityCount, standard: len(chosen) - priorityCount}, nil
}

// assignWithinCap writes the assignment with the cap enforced inside it, under
// a row lock on the bed so two callers cannot both fill the same free place.
func (s *Server) assignWithinCap(
	ctx context.Context, batchID uuid.UUID, ids []uuid.UUID, cap int,
) (int64, error) {
	var rows int64
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.LockBatchRow(ctx, batchID); err != nil {
			return err
		}
		n, err := q.AssignUnbatchedJobsWithinCap(ctx, gen.AssignUnbatchedJobsWithinCapParams{
			BatchID: &batchID, JobIds: ids, MaxUnits: int32(cap),
		})
		rows = n
		return err
	})
	return rows, err
}

// rebuildAfterTopUp re-plates a bed and re-reserves filament for what is now on
// it - the same closing sequence finishBatchEdit runs, without a gin context.
func (s *Server) rebuildAfterTopUp(ctx context.Context, batch gen.Batch) error {
	log := obs.FromContext(ctx)
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return err
	}
	if _, err := s.store.Q.ClearBatchPlateForEdit(ctx, batch.ID); err != nil {
		return err
	}

	units := int32(unitsOf(jobs))
	filament := sumFilament(jobs)
	total, effective := batchTimeFromJobs(jobs)
	params := gen.UpdateBatchDerivedMetricsParams{
		ID: batch.ID, UnitsPerBed: &units, TotalFilamentGrams: &filament,
		TotalPrintTimeMinutes: total, EffectiveTimePerUnitMinutes: effective,
	}
	if plate, ok := s.replatedBatch(ctx, batch, jobs); ok {
		params.PreviewFileID = &plate.fileID
		params.UnitsPerBed = int32ptr(plate.unitsPerBed)
		params.BedUtilizationPercent = &plate.utilisation
	}
	if _, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, params); err != nil {
		return err
	}

	// beginBatchEdit credited the old composition's filament back; this debits
	// the new one. Warn rather than fail: the bed is correct either way, and a
	// stock figure is recoverable where a plate on the wrong machine is not.
	if batch.FilamentReserved {
		if err := s.adjustFilamentByColour(ctx, s.store.Q, filamentSplitForJobs(jobs), -1); err != nil {
			log.Warn("could not re-reserve filament after a top-up",
				"batch", batch.BatchNumber, "error", err)
		}
	}
	s.triggerDispatch(ctx)
	return nil
}

// topUpCandidates picks the jobs for one bed: expedited work first, then
// standard work of the same configuration to fill what is left.
//
// Pure, so the rules below are testable without a database or a printer.
//
// Returns nothing unless at least one PRIORITY job goes on. A locked bed is a
// committed plate; opening one costs a withdrawal from the printer queue and a
// re-plate, and that is only worth paying for expedited work. Standard work can
// wait for a Draft, which costs nothing.
func topUpCandidates(
	key production.CompatibilityKey, room int, pool []gen.ProductionJob, taken map[uuid.UUID]bool,
) ([]gen.ProductionJob, int) {
	if room <= 0 {
		return nil, 0
	}
	var chosen []gen.ProductionJob
	priorityCount := 0
	left := room

	fits := func(j gen.ProductionJob) bool {
		if taken[j.ID] || compatibilityKeyOf(j) != key {
			return false
		}
		// No splitting. Minting a fragment row here would leave the planner a
		// job it has to reconcile, outside its own transaction. A job too big
		// for what is left falls through and forms its own bed, which is the
		// planner working correctly.
		return int(jobQuantity(j.Quantity)) <= left
	}

	for _, j := range pool {
		if j.Priority >= NormalRank || !fits(j) {
			continue
		}
		chosen = append(chosen, j)
		left -= int(jobQuantity(j.Quantity))
		priorityCount++
	}
	if priorityCount == 0 {
		return nil, 0
	}

	// Now fill the rest. The withdrawal and the re-plate are already paid for,
	// so topping the bed to its cap is free - and doing it in one pass avoids
	// withdrawing the same plate from the same printer queue twice.
	inChosen := make(map[uuid.UUID]bool, len(chosen))
	for _, j := range chosen {
		inChosen[j.ID] = true
	}
	for _, j := range pool {
		if left == 0 {
			break
		}
		if inChosen[j.ID] || !fits(j) {
			continue
		}
		chosen = append(chosen, j)
		left -= int(jobQuantity(j.Quantity))
	}
	return chosen, priorityCount
}

// anyPriority reports whether the pool holds expedited work at all.
func anyPriority(pool []gen.ProductionJob) bool {
	for _, j := range pool {
		if j.Priority < NormalRank {
			return true
		}
	}
	return false
}
