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

var testWeights = MachineScoreWeights{
	MaterialMatchBonus: 30 * time.Minute, ColourMatchBonus: 15 * time.Minute,
	MaterialChangePenalty: 45 * time.Minute, QueueLengthPenalty: 10 * time.Minute,
	HealthBonus: 10 * time.Minute,
}

func TestEffectiveFreeAtMaterialMatchAppliesBonus(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	in := MachineScoreInputs{LoadedMaterial: "PLA", Healthy: false}
	got := EffectiveFreeAt(c, in, "PLA", nil, testWeights)
	want := now.Add(-30 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("EffectiveFreeAt = %s, want %s (material match bonus)", got, want)
	}
}

func TestEffectiveFreeAtMaterialMismatchAppliesPenalty(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	in := MachineScoreInputs{LoadedMaterial: "PLA", Healthy: false}
	got := EffectiveFreeAt(c, in, "PETG", nil, testWeights)
	want := now.Add(45 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("EffectiveFreeAt = %s, want %s (material change penalty)", got, want)
	}
}

func TestEffectiveFreeAtUnknownMaterialAppliesNeitherBonusNorPenalty(t *testing.T) {
	// The most important edge case: an idle machine with nothing loaded/
	// queued must never be treated as a mismatch just for being empty.
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	in := MachineScoreInputs{Healthy: false}
	got := EffectiveFreeAt(c, in, "PLA", nil, testWeights)
	if !got.Equal(now) {
		t.Errorf("EffectiveFreeAt = %s, want unchanged %s (no signal, no adjustment)", got, now)
	}
}

func TestEffectiveFreeAtColourMatchScaledByOverlapFraction(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	// Loaded {Red}, batch needs {Red, Blue} - half overlap, half the colour bonus.
	in := MachineScoreInputs{LoadedMaterial: "PLA", LoadedColours: []string{"Red"}, Healthy: false}
	got := EffectiveFreeAt(c, in, "PLA", []string{"Red", "Blue"}, testWeights)
	// -30min material match bonus, -7.5min (half of the 15min colour bonus).
	want := now.Add(-30 * time.Minute).Add(-7*time.Minute - 30*time.Second)
	if !got.Equal(want) {
		t.Errorf("EffectiveFreeAt = %s, want %s (material bonus + half colour bonus)", got, want)
	}
}

func TestEffectiveFreeAtQueueLengthPenaltyAccumulates(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	in := MachineScoreInputs{QueueLength: 3, Healthy: false}
	got := EffectiveFreeAt(c, in, "PLA", nil, testWeights)
	want := now.Add(30 * time.Minute) // 3 * 10min
	if !got.Equal(want) {
		t.Errorf("EffectiveFreeAt = %s, want %s (3x queue length penalty)", got, want)
	}
}

func TestEffectiveFreeAtHealthBonusForOnlineOverBusy(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	c := MachineCandidate{FreeAt: now}
	healthy := EffectiveFreeAt(c, MachineScoreInputs{Healthy: true}, "PLA", nil, testWeights)
	busy := EffectiveFreeAt(c, MachineScoreInputs{Healthy: false}, "PLA", nil, testWeights)
	if !healthy.Before(busy) {
		t.Errorf("healthy EffectiveFreeAt %s should be earlier than busy %s", healthy, busy)
	}
}

// TestBestMachinePicksLowestEffectiveFreeAt mirrors the user's own worked
// example: don't send PETG Blue to a machine already loaded with PLA
// White/Black unless the time saved is worth the changeover. Proves scoring
// is a genuine trade-off, not an override: the mismatched-but-sooner machine
// wins when the free-time gap exceeds the penalty, loses when it doesn't.
func TestBestMachinePicksLowestEffectiveFreeAt(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	sooner, loaded := uuid.New(), uuid.New()
	candidates := []MachineCandidate{
		{MachineID: sooner, FreeAt: now},                       // free now, but loaded with PLA
		{MachineID: loaded, FreeAt: now.Add(20 * time.Minute)}, // free later, but already loaded with PETG
	}
	inputs := map[uuid.UUID]MachineScoreInputs{
		sooner: {LoadedMaterial: "PLA", Healthy: true},
		loaded: {LoadedMaterial: "PETG", Healthy: true},
	}

	// Gap (20min) is smaller than the match bonus (30min) - the match wins.
	best := BestMachine(candidates, inputs, "PETG", nil, testWeights)
	if best == nil || best.MachineID != loaded {
		t.Errorf("expected the already-loaded machine to win a 20min gap, got %+v", best)
	}

	// Widen the gap past the bonus - the sooner machine wins despite the changeover.
	candidates[1].FreeAt = now.Add(90 * time.Minute)
	best = BestMachine(candidates, inputs, "PETG", nil, testWeights)
	if best == nil || best.MachineID != sooner {
		t.Errorf("expected the sooner machine to win a 90min gap despite the changeover, got %+v", best)
	}
}

func TestBestMachineEmpty(t *testing.T) {
	if got := BestMachine(nil, nil, "PLA", nil, testWeights); got != nil {
		t.Fatalf("expected nil for no candidates, got %+v", got)
	}
}
