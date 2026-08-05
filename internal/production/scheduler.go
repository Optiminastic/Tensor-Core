package production

// Stage 9's "Earliest Available Machine Scheduling": given a fleet machine's
// current print (if any) and whatever is already queued behind it, compute
// when it will actually be free, then rank candidates by that time - not by
// which machine happens to be idle right now (a machine idle for the next 2
// minutes but with a 3-hour queue behind it is not "free").

import (
	"time"

	"github.com/google/uuid"
)

// FleetMachineState is the live print state of one physical fleet machine, as
// far as the scheduler needs to know it.
type FleetMachineState struct {
	MachineID             uuid.UUID
	PrintStartedAt        *time.Time
	BatchTotalTimeMinutes *int
}

// QueuedBatch is one batch already queued (open/in_progress) on a machine,
// ahead of whatever the scheduler is placing next.
type QueuedBatch struct {
	TotalPrintTimeMinutes int
}

// MachineFreeAt computes when a machine will next be free: now, plus whatever
// remains of its current print (if any), plus every already-queued batch's
// full time (queued batches run FCFS, one at a time, after the current print).
func MachineFreeAt(now time.Time, m FleetMachineState, queued []QueuedBatch) time.Time {
	freeAt := now
	if m.PrintStartedAt != nil && m.BatchTotalTimeMinutes != nil {
		remaining := time.Duration(*m.BatchTotalTimeMinutes)*time.Minute - now.Sub(*m.PrintStartedAt)
		if remaining > 0 {
			freeAt = now.Add(remaining)
		}
	}
	for _, b := range queued {
		freeAt = freeAt.Add(time.Duration(b.TotalPrintTimeMinutes) * time.Minute)
	}
	return freeAt
}

// MachineCandidate is one machine the scheduler is choosing between, already
// resolved to how soon it is free.
type MachineCandidate struct {
	MachineID uuid.UUID
	ProfileID uuid.UUID
	FreeAt    time.Time
}

// EarliestFreeMachine picks the candidate that is free soonest. Returns nil
// for an empty candidate list (nothing eligible - e.g. no fleet machine online
// for the required family).
func EarliestFreeMachine(candidates []MachineCandidate) *MachineCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.FreeAt.Before(best.FreeAt) {
			best = c
		}
	}
	return &best
}
