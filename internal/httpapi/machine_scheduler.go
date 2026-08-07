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
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// assignMachineForBatch returns the machine_profiles id of whichever online
// fleet machine matching family scores best for a batch needing material
// (and, if known, colours), for batches.machine_id (which points at
// machine_profiles, not machines - see 0024's comment). Scoring (Stage 6:
// Machine Assignment) ranks by BestMachine's material/colour-match-adjusted
// free time, not raw earliest-free alone - see internal/production/
// scheduler.go's EffectiveFreeAt for the weights. Returns nil when nothing
// is eligible (empty family, no fleet machine linked/online for it, or the
// lookup fails) - the batch is simply created unassigned, same as before
// this scheduler existed, and a human assigns one at approval.
func (s *Server) assignMachineForBatch(ctx context.Context, family, material string, colours []string) *uuid.UUID {
	if family == "" {
		return nil
	}
	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		return nil
	}

	now := time.Now()
	weights := production.MachineScoreWeights{
		MaterialMatchBonus:    time.Duration(s.cfg.MachineMaterialMatchBonusMinutes * float64(time.Minute)),
		ColourMatchBonus:      time.Duration(s.cfg.MachineColourMatchBonusMinutes * float64(time.Minute)),
		MaterialChangePenalty: time.Duration(s.cfg.MachineMaterialChangePenaltyMinutes * float64(time.Minute)),
		QueueLengthPenalty:    time.Duration(s.cfg.MachineQueueLengthPenaltyMinutes * float64(time.Minute)),
		HealthBonus:           time.Duration(s.cfg.MachineHealthBonusMinutes * float64(time.Minute)),
	}

	var candidates []production.MachineCandidate
	inputs := make(map[uuid.UUID]production.MachineScoreInputs)
	for _, r := range rows {
		if r.Status == "off" || r.MachineProfileID == nil {
			continue
		}
		if r.ProfileStatus != nil && (*r.ProfileStatus == production.MachineOffline || *r.ProfileStatus == production.MachineMaintenance) {
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
		queued := s.queuedBatchLoad(ctx, r.ID)
		candidates = append(candidates, production.MachineCandidate{
			MachineID: r.ID,
			ProfileID: *r.MachineProfileID,
			FreeAt:    production.MachineFreeAt(now, state, queued),
		})
		loadedMaterial, loadedColours := s.currentlyLoadedMaterial(ctx, r.CurrentBatchID, r.ID)
		inputs[r.ID] = production.MachineScoreInputs{
			LoadedMaterial: loadedMaterial, LoadedColours: loadedColours,
			QueueLength: len(queued),
			Healthy:     r.ProfileStatus == nil || *r.ProfileStatus == production.MachineOnline,
		}
	}

	best := production.BestMachine(candidates, inputs, material, colours, weights)
	if best == nil {
		return nil
	}
	return &best.ProfileID
}

// currentlyLoadedMaterial derives what a fleet machine's active work
// actually requires - its current batch's jobs if it's printing, or its
// next queued batch's jobs if idle - the same "what's this machine doing"
// signal computeLiveFilaments (fleet_machines.go) already derives for
// display, reused here for scoring instead. Returns ("", nil) when there's
// genuinely nothing to go on (idle, empty queue, or a lookup failure) -
// EffectiveFreeAt then applies neither a match bonus nor a change penalty,
// never penalising a genuinely available empty machine.
func (s *Server) currentlyLoadedMaterial(ctx context.Context, currentBatchID *uuid.UUID, fleetMachineID uuid.UUID) (string, []string) {
	if currentBatchID != nil {
		if jobs, err := s.store.Q.ListJobsForBatch(ctx, currentBatchID); err == nil && len(jobs) > 0 {
			return materialColoursOf(jobs)
		}
	}
	queued, err := s.store.Q.ListQueuedBatchesForFleetMachine(ctx, fleetMachineID)
	if err != nil || len(queued) == 0 {
		return "", nil
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &queued[0].ID)
	if err != nil || len(jobs) == 0 {
		return "", nil
	}
	return materialColoursOf(jobs)
}

// materialColoursOf reads a batch's jobs' shared material (compatibility
// grouping already guarantees they all match) and the union of colours
// across them.
func materialColoursOf(jobs []gen.ProductionJob) (string, []string) {
	var material string
	seen := map[string]bool{}
	var colours []string
	for _, j := range jobs {
		if material == "" && j.Material != nil {
			material = *j.Material
		}
		for _, c := range decodeColours(j.Colours) {
			if !seen[c] {
				seen[c] = true
				colours = append(colours, c)
			}
		}
	}
	return material, colours
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

// reassignBatchesForOfflineMachine runs right after a machine_profiles row is
// PATCHed to offline/maintenance: every Draft (pending_approval) batch still
// parked on that profile gets a fresh assignMachineForBatch pass - which, now
// that the profile's new status is already committed, naturally excludes it
// from the candidates - so each batch either lands on another eligible
// machine or, if none exists, is explicitly cleared to unassigned rather than
// left silently pointed at a dead machine. Approved (open) batches are a
// human commitment and are deliberately left alone; a human moves those via
// PATCH /batches/:id if needed. Best-effort like the rest of the scheduler:
// a lookup or write failure is logged, not propagated - the machine's status
// change itself already succeeded and must not be rolled back for this.
func (s *Server) reassignBatchesForOfflineMachine(ctx context.Context, machineProfileID uuid.UUID) {
	log := obs.FromContext(ctx)
	batches, err := s.store.Q.ListPendingApprovalBatchesForMachine(ctx, &machineProfileID)
	if err != nil {
		log.Warn("could not list draft batches for offline machine", "machine_id", machineProfileID, "error", err)
		return
	}
	for _, b := range batches {
		jobs, err := s.store.Q.ListJobsForBatch(ctx, &b.ID)
		if err != nil {
			log.Warn("could not load batch jobs for reassignment", "batch", b.ID, "error", err)
			continue
		}
		var family string
		if len(jobs) > 0 && jobs[0].MachineFamily != nil {
			family = *jobs[0].MachineFamily
		}
		material, colours := materialColoursOf(jobs)
		next := s.assignMachineForBatch(ctx, family, material, colours)
		if _, err := s.store.Q.UpdateBatch(ctx, gen.UpdateBatchParams{
			ID: b.ID, SetMachineID: true, MachineID: next,
		}); err != nil {
			log.Warn("could not reassign batch off offline machine", "batch", b.ID, "error", err)
			continue
		}
		if next == nil {
			log.Info("batch left unassigned after its machine went offline", "batch", b.ID, "family", family)
		} else {
			log.Info("batch reassigned after its machine went offline", "batch", b.ID, "from_machine", machineProfileID, "to_machine", *next)
		}
	}
	if len(batches) > 0 {
		// A machine just went offline - low-frequency, high-signal, same
		// category as batch-completed/reprint-created (see triggerBatchPlan's
		// doc comment): worth an immediate look regardless of backlog size,
		// since a batch above may have just been cleared to fully unassigned.
		s.triggerBatchPlan(ctx)
	}
}
