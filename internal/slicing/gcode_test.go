package slicing

import (
	"os"
	"testing"
)

// Ported from the Python worker's tests/test_gcode.py against the same fixtures.

// A minimal Bambu-style gcode: relative E, one support region and one wall region.
const supportGcode = `; FEATURE: Support
G1 X1 Y1 E3
G1 X2 Y2 E1
; FEATURE: Outer wall
G1 X3 Y3 E6
G1 F3000
`

func TestWeightFromLengthPLA(t *testing.T) {
	// 1232.42mm of 1.75mm PLA (1.24 g/cm^3) -> ~3.68 g.
	if got := WeightFromLength(1232.42, 1.24); got != 3.68 {
		t.Errorf("WeightFromLength = %v, want 3.68", got)
	}
}

func TestSupportFraction(t *testing.T) {
	// Support E = 3 + 1 = 4; total E = 4 + 6 = 10 -> 40%.
	if got := SupportFraction(supportGcode); got != 0.4 {
		t.Errorf("SupportFraction = %v, want 0.4", got)
	}
	if got := SupportFraction("; FEATURE: Outer wall\nG1 X1 Y1 E5\n"); got != 0 {
		t.Errorf("SupportFraction(no support) = %v, want 0", got)
	}
	if got := SupportFraction(""); got != 0 {
		t.Errorf("SupportFraction(empty) = %v, want 0", got)
	}
}

func loadFixture(t *testing.T) (ResultJSON, string) {
	t.Helper()
	result, err := LoadResultJSON("testdata/result.json")
	if err != nil {
		t.Fatalf("LoadResultJSON: %v", err)
	}
	sliceInfo, err := os.ReadFile("testdata/slice_info.config")
	if err != nil {
		t.Fatalf("read slice_info: %v", err)
	}
	return result, string(sliceInfo)
}

func TestExtractMetricsFromRealSlice(t *testing.T) {
	result, sliceInfo := loadFixture(t)
	m, err := ExtractMetrics(result, sliceInfo, 1.24, "")
	if err != nil {
		t.Fatalf("ExtractMetrics: %v", err)
	}
	if m.PrintTimeS < 615 || m.PrintTimeS > 625 { // ~619s
		t.Errorf("PrintTimeS = %v, want ~619", m.PrintTimeS)
	}
	if m.FilamentLengthMM < 1200 || m.FilamentLengthMM > 1260 { // ~1230mm
		t.Errorf("FilamentLengthMM = %v, want ~1230", m.FilamentLengthMM)
	}
	if m.FilamentWeightG < 3.4 || m.FilamentWeightG > 3.9 { // ~3.67g
		t.Errorf("FilamentWeightG = %v, want ~3.67", m.FilamentWeightG)
	}
	if d := m.LayerHeightMM - 0.2; d < -0.01 || d > 0.01 {
		t.Errorf("LayerHeightMM = %v, want 0.2", m.LayerHeightMM)
	}
	if m.InfillDensityPct != 20 {
		t.Errorf("InfillDensityPct = %v, want 20", m.InfillDensityPct)
	}
	if m.WallLoops != 2 {
		t.Errorf("WallLoops = %v, want 2", m.WallLoops)
	}
	if m.SupportUsed {
		t.Errorf("SupportUsed = true, want false")
	}
	if m.SupportWeightG != 0 || m.PurgeWeightG != 0 || m.ColourChanges != 0 {
		t.Errorf("expected zero support/purge/colour, got %+v", m)
	}
}

func TestExtractMetricsSupportShare(t *testing.T) {
	result, sliceInfo := loadFixture(t)
	m, err := ExtractMetrics(result, sliceInfo, 1.24, supportGcode)
	if err != nil {
		t.Fatalf("ExtractMetrics: %v", err)
	}
	// 40% of the total filament weight is attributed to support.
	if want := round2(m.FilamentWeightG * 0.4); m.SupportWeightG != want {
		t.Errorf("SupportWeightG = %v, want %v", m.SupportWeightG, want)
	}
}

func TestExtractMetricsRejectsFailedSlice(t *testing.T) {
	rc := -66
	failed := ResultJSON{ReturnCode: &rc, ErrorString: "mapping failed"}
	_, err := ExtractMetrics(failed, "<config/>", 1.24, "")
	if err == nil || err.Error() != "mapping failed" {
		t.Errorf("expected 'mapping failed' error, got %v", err)
	}
}
