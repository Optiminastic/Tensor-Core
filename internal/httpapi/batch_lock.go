package httpapi

// When a bed stops being a proposal and becomes a commitment.
//
// Under colour batching a bed holds one colour and at most bedUnitCap products.
// It stays a Draft until it holds at least bedUnitFloor of them AND a printer
// of its class is free with the right colours loaded. Both halves matter, and
// the second is the one that is easy to leave out.
//
// A Draft is the only state that can still absorb work: the planner dissolves
// and reforms Drafts on every run, which is exactly what lets a bed that formed
// with two blue planks take a third when the next blue order arrives (see
// ListReplannableJobs). So staying a Draft is not the bed waiting around - it
// is the bed still growing.
//
// Locking is the opposite, and it is one-way. It merges the plate, reserves the
// filament, stamps a machine and takes the jobs out of the replanning pool for
// good. Doing that while no printer can take the bed is the worst of both: the
// plate cannot print AND it cannot grow. So the lock is held until the fleet
// can actually have it, at which point freezing costs nothing - the bed is on
// its way to a machine within the same pass.
//
// A bed that never reaches the floor, or whose colour is loaded nowhere, waits
// indefinitely. An operator can still approve one by hand when the wait stops
// being worth it - see readyToLock.

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bedUnitCap is the fullest a bed gets.
//
// One number for the whole fleet, and one source of truth, so "full" means the
// same thing to the planner, the dispatcher and the add-jobs endpoint.
//
// The class is no longer independent of this - a bed of five is laid out on an
// H2C's bed, four on an A2L's, three on a P2S's, because the plate's offsets
// are fixed and a plate laid out for one class cannot print on a smaller one.
// But the COUNT decides the class rather than the other way round, which is
// what the reverted attempt had backwards: it sized the bed from live fleet
// state, so the contents of a plate depended on which printer happened to be
// free.
func (s *Server) bedUnitCap() int {
	if s.cfg.BatchMaxUnitsPerBed > 0 {
		return s.cfg.BatchMaxUnitsPerBed
	}
	return production.MaxBedUnits
}

// bedUnitFloor is how empty a bed may be and still be worth printing.
//
// A plate with one plank on it costs the same machine-hour as a plate with
// five, so a bed under this waits for company.
//
// NO EXCEPTIONS. A bed carrying a priority job used to be one - somebody had
// paid to jump the queue, and waiting for company spent that money on nothing -
// but batching is first come, first served now, so an expedited bed waits for
// its third plank like any other. Clearing the floor is also only half of it:
// see readyToLock, which additionally wants a free printer.
func (s *Server) bedUnitFloor() int {
	if s.cfg.BatchMinUnitsPerBed > 0 {
		return s.cfg.BatchMinUnitsPerBed
	}
	return production.MinBedUnits
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

// lockFullBatches approves every newly-planned bed a printer can take.
//
// Best-effort per bed and deliberately after the planning transaction: approval
// merges the plate, reserves filament and enqueues a slice, none of which
// belongs inside the transaction that assigns jobs. A bed that cannot be
// approved yet (no machine online, a job put on hold since planning) simply
// stays a Draft and the dispatch pass tries it again.
func (s *Server) lockFullBatches(ctx context.Context, created []gen.Batch) {
	log := obs.FromContext(ctx)
	// One fleet read for the whole run, shared by every bed below. Taken here
	// rather than inside readyToLock because planning can produce dozens of
	// beds and the snapshot costs three BambuBuddy round-trips.
	snap := s.fleetSnapshotNow(ctx)
	for _, b := range created {
		if b.Status != production.BatchPendingApproval {
			continue
		}
		// Re-read, because the row in hand is stale in the one field the gate
		// needs. These batches were returned by the planning transaction;
		// cachePreview has since written each one's preview_file_id, and the
		// copy here still says nil. Without this re-read the gate finds no
		// plate to bind to trays and holds every bed it was called for -
		// silently costing each new bed a whole dispatch interval.
		fresh, err := s.store.Q.GetBatchByID(ctx, b.ID)
		if err != nil {
			log.Info("could not re-read a new bed before locking it, leaving it as a Draft",
				"batch", b.BatchNumber, "error", err)
			continue
		}
		b = fresh
		// The same rule the dispatcher applies, so a bed does not have to wait
		// for the next pass to be committed on grounds that already hold.
		if !s.readyToLock(ctx, b, snap) {
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
		log.Info("bed locked", "batch", b.BatchNumber, "units", units, "cap", s.bedUnitCap())
	}
}

// readyToLock reports whether a Draft should be committed now.
//
// TWO conditions, both required, both the shop's own words: the bed holds at
// least bedUnitFloor products, AND a printer of its class is standing empty
// with the right colours loaded.
//
// The floor, not the cap. Three, four and five are all real beds - they simply
// go to different classes of printer - so holding a bed of three until it
// reaches five would be holding a bed that is ready. Under the floor it waits
// for company, because a plate with one plank on it costs the same machine-hour
// as a plate with five.
//
// The free-printer half is the new one, and it is what makes the floor safe to
// act on. A bed of three used to lock the moment it formed and then sat
// "locked, and no printer can take it" - its filament reserved, its jobs out of
// the replanning pool, unable to absorb the fourth order in its colour that
// arrived an hour later. Waiting for a machine instead costs nothing and keeps
// the bed growing: the planner dissolves and reforms Drafts on every run, so
// the next order in that colour joins this bed rather than opening one of its
// own. See bedHasFreePrinter.
//
// Two escapes have been REMOVED, both deliberately and both on instruction:
//
//   - A clock. After BATCH_MAX_WAIT_HOURS a partial bed used to lock anyway. A
//     partial bed is a wasted plate, and waiting costs less than printing one.
//   - Expedited work. A bed carrying a priority plank used to lock under-full.
//     Batching is first come, first served now, so a priority bed stays an open
//     Draft and keeps absorbing jobs of its colour profile like any other.
//
// THE CONSEQUENCE, stated plainly: a colour that never reaches the floor never
// prints by itself, and a bed whose colour is loaded nowhere never locks. An
// operator can still approve either by hand - ApproveBatchFor has no fullness
// or fleet check, deliberately - so the judgement moves to a person rather than
// to a clock.
//
// Outside colour batching every Draft is ready: the optimiser's own gate already
// decided a bed was worth building before it produced one, so second-guessing it
// here would strand beds it deliberately released.
func (s *Server) readyToLock(ctx context.Context, b gen.Batch, snap *fleetSnapshot) bool {
	if s.batchStrategy() != production.StrategyColour {
		return true
	}
	log := obs.FromContext(ctx)

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &b.ID)
	if err != nil {
		// Held, not released. Approval no longer re-checks everything that
		// matters - it does not ask whether a printer is free - so guessing
		// "ready" here commits a plate on the strength of a failed read.
		// Holding a Draft costs one pass; the next one re-reads it.
		log.Warn("could not read a bed's jobs, holding it open", "batch", b.BatchNumber, "error", err)
		return false
	}
	if units := unitsOf(jobs); units < s.bedUnitFloor() {
		return false
	}
	machineID, ok, why := s.bedHasFreePrinter(ctx, b, snap)
	if !ok {
		// Info: a bed waiting for a machine is the system working, not a fault.
		log.Info("bed held open, no printer can take it yet",
			"batch", b.BatchNumber, "reason", why)
		return false
	}
	// Taken for the rest of this pass, so the next bed in it does not see the
	// same idle printer and lock against it too. One batch per machine has to
	// hold WITHIN a pass as well as between them.
	snap.claim(machineID)
	return true
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
