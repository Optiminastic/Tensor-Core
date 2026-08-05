package httpapi

// Stage 9's Earliest Available Machine Scheduling: resolve a batch's required
// machine family (e.g. "H2C") to a specific physical fleet machine by ranking
// every online, linked machine by internal/production.MachineFreeAt. Lives in
// httpapi (not internal/production) for the same reason batch_orchestrate.go
// does - it reads the database.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// assignMachineForBatch returns the machine_profiles id of whichever online
// fleet machine matching family is free soonest, for batches.machine_id (which
// points at machine_profiles, not machines - see 0024's comment). Returns nil
// when nothing is eligible (empty family, no fleet machine linked/online for
// it, or the lookup fails) - the batch is simply created unassigned, same as
// before this scheduler existed, and a human assigns one at approval.
func (s *Server) assignMachineForBatch(ctx context.Context, family string) *uuid.UUID {
	if family == "" {
		return nil
	}
	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		return nil
	}

	now := time.Now()
	var candidates []production.MachineCandidate
	for _, r := range rows {
		if r.Status == "off" || r.MachineProfileID == nil {
			continue
		}
		if r.ProfileFamily == nil || *r.ProfileFamily != family {
			continue
		}
		state := production.FleetMachineState{
			MachineID:             r.ID,
			PrintStartedAt:        db.TimePtr(r.PrintStartedAt),
			BatchTotalTimeMinutes: int32PtrToIntPtr(r.BatchTotalTimeMinutes),
		}
		candidates = append(candidates, production.MachineCandidate{
			MachineID: r.ID,
			ProfileID: *r.MachineProfileID,
			FreeAt:    production.MachineFreeAt(now, state, s.queuedBatchLoad(ctx, r.ID)),
		})
	}

	best := production.EarliestFreeMachine(candidates)
	if best == nil {
		return nil
	}
	return &best.ProfileID
}

// queuedBatchLoad reads whatever is already open/in_progress on one fleet
// machine. A lookup failure is treated as "nothing queued" rather than
// disqualifying the machine - the scheduler is a best-effort placement, not a
// hard guarantee, and a human can always override at approval.
func (s *Server) queuedBatchLoad(ctx context.Context, fleetMachineID uuid.UUID) []production.QueuedBatch {
	rows, err := s.store.Q.ListQueuedBatchesForFleetMachine(ctx, fleetMachineID)
	if err != nil {
		return nil
	}
	queued := make([]production.QueuedBatch, 0, len(rows))
	for _, b := range rows {
		if b.TotalPrintTimeMinutes != nil {
			queued = append(queued, production.QueuedBatch{TotalPrintTimeMinutes: int(*b.TotalPrintTimeMinutes)})
		}
	}
	return queued
}
