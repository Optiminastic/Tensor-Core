package pricing

import (
	"math"
	"testing"
)

func TestEstimateTextVolume(t *testing.T) {
	cfg := DefaultEstimationConfig()
	base := PersonalisationSpec{Text: "ARKIT", HeightMM: 8, DepthMM: 1.5}

	v := EstimateTextVolume(base, cfg)
	if v <= 0 {
		t.Fatalf("volume = %v, want > 0", v)
	}
	// Twice the characters -> twice the volume.
	if got := EstimateTextVolume(PersonalisationSpec{Text: "ARKITARKIT", HeightMM: 8, DepthMM: 1.5}, cfg); math.Abs(got-2*v) > 1e-6 {
		t.Errorf("10 chars = %v, want 2x %v", got, v)
	}
	// Double the height -> 4x the volume (grows in two dimensions).
	if got := EstimateTextVolume(PersonalisationSpec{Text: "ARKIT", HeightMM: 16, DepthMM: 1.5}, cfg); math.Abs(got-4*v) > 1e-6 {
		t.Errorf("2x height = %v, want 4x %v", got, v)
	}
	// Spaces are not printed.
	if got := EstimateTextVolume(PersonalisationSpec{Text: "A R K I T", HeightMM: 8, DepthMM: 1.5}, cfg); math.Abs(got-v) > 1e-6 {
		t.Errorf("spaced name = %v, want same as %v", got, v)
	}
	// Empty / zero size -> zero.
	if got := EstimateTextVolume(PersonalisationSpec{Text: "", HeightMM: 8, DepthMM: 1.5}, cfg); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
}

func TestEstimatePersonalisation(t *testing.T) {
	cfg := DefaultEstimationConfig()
	base := SlicerMetrics{
		PrintTimeHr: 4, FilamentG: 52, PurgeG: 1, UnitsPerBed: 1, EffectiveMachineTimeHr: 4,
	}
	out, delta := EstimatePersonalisation(base, PersonalisationSpec{Text: "ARKIT", HeightMM: 8, DepthMM: 1.5}, DensityForMaterial("PLA"), cfg)

	if delta.GramsAdded <= 0 || delta.TimeAddedHr <= 0 || delta.VolumeMM3 <= 0 {
		t.Fatalf("delta = %+v, want all positive", delta)
	}
	if math.Abs(out.FilamentG-(base.FilamentG+delta.GramsAdded)) > 1e-9 {
		t.Errorf("FilamentG = %v, want base + added", out.FilamentG)
	}
	if math.Abs(out.EffectiveMachineTimeHr-(base.EffectiveMachineTimeHr+delta.TimeAddedHr)) > 1e-9 {
		t.Errorf("EffectiveMachineTimeHr = %v, want base + added", out.EffectiveMachineTimeHr)
	}
	// grams = volume(cm3) x density; density PLA = 1.24.
	if math.Abs(delta.GramsAdded-(delta.VolumeMM3/1000*1.24)) > 1e-9 {
		t.Errorf("grams %v inconsistent with volume %v", delta.GramsAdded, delta.VolumeMM3)
	}
	// Base fields the estimate doesn't touch are preserved.
	if out.UnitsPerBed != base.UnitsPerBed || out.PurgeG != base.PurgeG {
		t.Errorf("estimate mutated an unrelated field: %+v", out)
	}
}
