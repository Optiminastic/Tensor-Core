package designadvice

import (
	"strings"
	"testing"
)

func hours(h float64) *float64 { return &h }

func TestSuggestGreenCleanDesign(t *testing.T) {
	got := Suggest(Input{
		Verdict: "green", FilamentG: 20, EffectiveMachineTimeHr: 0.5,
		InfillPct: 15, SupportUsed: false, UnitsPerBed: 4, EntryMachineHours: hours(2),
	})
	if len(got) != 0 {
		t.Fatalf("clean green design should yield no suggestions, got %v", got)
	}
}

func TestSuggestFlagsSupportInfillAndTime(t *testing.T) {
	got := Suggest(Input{
		Verdict: "yellow", FilamentG: 120, EffectiveMachineTimeHr: 3.0,
		InfillPct: 40, SupportUsed: true, UnitsPerBed: 1, EntryMachineHours: hours(2),
	})
	if len(got) != 4 {
		t.Fatalf("expected 4 suggestions (support, infill, time, filament), got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "support") {
		t.Errorf("support suggestion should come first, got %q", got[0])
	}
}

func TestSuggestRedFallback(t *testing.T) {
	got := Suggest(Input{
		Verdict: "red", FilamentG: 10, EffectiveMachineTimeHr: 0.4,
		InfillPct: 15, SupportUsed: false, UnitsPerBed: 4, EntryMachineHours: hours(2),
	})
	if len(got) != 1 || !strings.Contains(got[0], "too high") {
		t.Fatalf("red with no specific flags should give the generic lever hint, got %v", got)
	}
}
