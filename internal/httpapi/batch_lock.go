package httpapi

// When a bed stops being a proposal and becomes a commitment.
//
// Under colour batching a bed holds one colour and at most four products. Until
// it holds four it is deliberately still a Draft: the planner dissolves and
// reforms Drafts on every run, which is exactly what lets a bed that formed with
// two blue planks absorb a third when the next blue order arrives (see
// ListReplannableJobs). A bed frozen at two would make the customer behind it
// wait for a whole new bed.
//
// The moment it holds four there is nothing left to absorb, so freezing it is
// not a loss - and leaving it a Draft would be: the next planner run could
// dissolve those four and rearrange them, changing a plate somebody may already
// be looking at. So a full bed is approved on the spot, which reserves its
// filament, stamps its machine, queues its plate slice and takes it out of the
// replanning pool for good.
//
// A bed that never fills waits. That is deliberate: a partial plate is a wasted
// one, so an under-full bed stays a Draft indefinitely, absorbing the next order
// in its colour. An operator can still approve one by hand when the wait stops
// being worth it - see readyToLock.

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bedUnitCap is how many products may share one bed.
//
// One number for the whole fleet, and one source of truth, so "full" means the
// same thing to the planner, the dispatcher and the add-jobs endpoint. Deriving
// it from the machine class was tried and reverted: a plate is built before
// anything knows which printer will take it, so a bed sized for one class is a
// bed the others cannot print.
func (s *Server) bedUnitCap() int {
	if s.cfg.BatchMaxUnitsPerBed > 0 {
		return s.cfg.BatchMaxUnitsPerBed
	}
	return production.MaxColourBatchUnits
}

// unitsOnBed counts the products on a bed, quantity included: one job for three
// of the same plank fills three of the six places, not one.
func (s *Server) unitsOnBed(ctx context.Context, batchID uuid.UUID) (int, error) {
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		return 0, err
	}
	return unitsOf(jobs), nil
}

// unitsOf is the quantity-expanded product count of a bed's jobs.
func unitsOf(jobs []gen.ProductionJob) int {
	units := 0
	for _, j := range jobs {
		units += int(jobQuantity(j.Quantity))
	}
	return units
}

// bedIsFull reports whether a bed has no room left.
//
// Counted from the jobs rather than read from units_per_bed, which is a derived
// column written at plan time and can lag a job being added or removed by hand.
func (s *Server) bedIsFull(ctx context.Context, batchID uuid.UUID) (bool, error) {
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		return false, err
	}
	return unitsOf(jobs) >= s.bedUnitCap(), nil
}

// lockFullBatches approves every newly-planned bed that is already full.
//
// Best-effort per bed and deliberately after the planning transaction: approval
// merges the plate, reserves filament and enqueues a slice, none of which
// belongs inside the transaction that assigns jobs. A bed that cannot be
// approved yet (no machine online, a job put on hold since planning) simply
// stays a Draft and the dispatch pass tries it again.
func (s *Server) lockFullBatches(ctx context.Context, created []gen.Batch) {
	log := obs.FromContext(ctx)
	for _, b := range created {
		if b.Status != production.BatchPendingApproval {
			continue
		}
		full, err := s.bedIsFull(ctx, b.ID)
		if err != nil {
			log.Warn("could not tell whether a new bed is full", "batch", b.BatchNumber, "error", err)
			continue
		}
		if !full {
			continue
		}
		units, _ := s.unitsOnBed(ctx, b.ID)
		if _, err := s.ApproveBatchFor(ctx, b.ID, nil, systemActor); err != nil {
			// Info, not Warn: the overwhelmingly common cause is that no
			// machine is online for this bed's family yet, which the next
			// dispatch pass resolves on its own.
			log.Info("a full bed could not be locked yet, leaving it as a Draft",
				"batch", b.BatchNumber, "error", err)
			continue
		}
		log.Info("bed full, locked", "batch", b.BatchNumber, "units", units)
	}
}

// readyToLock reports whether a Draft should be committed now.
//
// Full, and only full. A bed of three waits for a fourth however long that
// takes, because a Draft is the only state that can still absorb one: the
// planner dissolves and reforms Drafts on every run, so the next order in that
// colour joins the bed instead of opening one of its own.
//
// There used to be an escape - after BATCH_MAX_WAIT_HOURS a partial bed locked
// anyway, so a lone plank in an unpopular colour was not held for company that
// never arrived. That is gone at the shop's instruction: a partial bed is a
// wasted plate, and waiting costs less than printing one.
//
// THE CONSEQUENCE, stated plainly: a colour that never reaches four never prints
// by itself. An operator can still approve such a bed by hand - ApproveBatchFor
// has no fullness check, deliberately - so the judgement moves to a person
// rather than to a clock.
//
// Outside colour batching every Draft is ready: the optimiser's own gate already
// decided a bed was worth building before it produced one, so second-guessing it
// here would strand beds it deliberately released.
func (s *Server) readyToLock(ctx context.Context, b gen.Batch) bool {
	if s.batchStrategy() != production.StrategyColour {
		return true
	}
	full, err := s.bedIsFull(ctx, b.ID)
	if err != nil {
		// Unknown is not "no": failing to read the bed must not strand it
		// permanently, and approval re-checks everything that matters anyway.
		obs.FromContext(ctx).Warn("could not tell whether a bed is full, treating it as ready",
			"batch", b.BatchNumber, "error", err)
		return true
	}
	return full
}

// triggerDispatch schedules a pass that walks ready beds onto printers.
//
// Best-effort, like triggerBatchPlan: the beds exist either way, and the
// periodic pass would find them eventually. This is what makes "eventually"
// mean seconds instead of minutes - and what keeps the flow working when
// another process holds River leadership and owns the periodic ticks.
func (s *Server) triggerDispatch(ctx context.Context) {
	if s.dispatchEnqueuer == nil || !s.cfg.BatchAutoDispatch {
		return
	}
	if err := s.dispatchEnqueuer.Enqueue(ctx); err != nil {
		obs.FromContext(ctx).Error("could not schedule a batch dispatch pass", "error", err)
	}
}
