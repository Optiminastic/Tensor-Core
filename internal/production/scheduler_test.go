package production

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMachineFreeAtNoCurrentPrint(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	freeAt := MachineFreeAt(now, FleetMachineState{}, nil)
	if !freeAt.Equal(now) {
		t.Fatalf("expected free now, got %s", freeAt)
	}
}

func TestMachineFreeAtRemainingCurrentPrint(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-40 * time.Minute)
	total := 60
	freeAt := MachineFreeAt(now, FleetMachineState{PrintStartedAt: &startedAt, BatchTotalTimeMinutes: &total}, nil)
	want := now.Add(20 * time.Minute)
	if !freeAt.Equal(want) {
		t.Fatalf("expected free at %s, got %s", want, freeAt)
	}
}

func TestMachineFreeAtCurrentPrintAlreadyOverdue(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-90 * time.Minute)
	total := 60
	freeAt := MachineFreeAt(now, FleetMachineState{PrintStartedAt: &startedAt, BatchTotalTimeMinutes: &total}, nil)
	if !freeAt.Equal(now) {
		t.Fatalf("expected free now (overdue print), got %s", freeAt)
	}
}

func TestMachineFreeAtAddsQueuedBatches(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	queued := []QueuedBatch{{TotalPrintTimeMinutes: 30}, {TotalPrintTimeMinutes: 15}}
	freeAt := MachineFreeAt(now, FleetMachineState{}, queued)
	want := now.Add(45 * time.Minute)
	if !freeAt.Equal(want) {
		t.Fatalf("expected free at %s, got %s", want, freeAt)
	}
}

// TestEarliestFreeMachinePicksSoonest mirrors the plan's worked example:
// Machine 1 free in 20 min, Machine 2 free now, Machine 3 free in 1 hour ->
// choose Machine 2.
func TestEarliestFreeMachinePicksSoonest(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	m1, m2, m3 := uuid.New(), uuid.New(), uuid.New()
	candidates := []MachineCandidate{
		{MachineID: m1, FreeAt: now.Add(20 * time.Minute)},
		{MachineID: m2, FreeAt: now},
		{MachineID: m3, FreeAt: now.Add(1 * time.Hour)},
	}
	best := EarliestFreeMachine(candidates)
	if best == nil || best.MachineID != m2 {
		t.Fatalf("expected machine 2, got %+v", best)
	}
}

func TestEarliestFreeMachineEmpty(t *testing.T) {
	if got := EarliestFreeMachine(nil); got != nil {
		t.Fatalf("expected nil for no candidates, got %+v", got)
	}
}
