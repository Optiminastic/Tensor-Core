package failurerisk

import "testing"

func TestAssessCleanDesignIsLow(t *testing.T) {
	a := Assess(Inputs{FilamentG: 40, SupportG: 0, SupportUsed: false, WallLoops: 3})
	if a.Band != "low" || a.Score >= bandMediumAt {
		t.Errorf("clean design should be low risk, got %+v", a)
	}
}

func TestAssessHeavySupportAndThinWallsIsHigh(t *testing.T) {
	a := Assess(Inputs{
		FilamentG:           40,
		SupportG:            16, // 40% of part
		SupportUsed:         true,
		WallLoops:           1,
		OverhangBaselineMM2: 3500,
	})
	if a.Band != "high" {
		t.Errorf("heavy support + thin walls + big overhang should be high, got %+v", a)
	}
	if a.Score < bandHighAt {
		t.Errorf("score = %d, want >= %d", a.Score, bandHighAt)
	}
}

func TestAssessScoreClampedAndBanded(t *testing.T) {
	a := Assess(Inputs{FilamentG: 10, SupportG: 10, WallLoops: 1, OverhangBaselineMM2: 9000, ColourChanges: 6, PurgeG: 5})
	if a.Score > 100 {
		t.Errorf("score not clamped: %d", a.Score)
	}
	if a.Band != "high" {
		t.Errorf("band = %q, want high", a.Band)
	}
	if len(a.Factors) == 0 {
		t.Error("expected contributing factors")
	}
}

func TestAssessModerateSupportIsMedium(t *testing.T) {
	a := Assess(Inputs{FilamentG: 40, SupportG: 8, SupportUsed: true, WallLoops: 2})
	if a.Band != "medium" {
		t.Errorf("moderate support + 2 walls should be medium, got %+v", a)
	}
}
