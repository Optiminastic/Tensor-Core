package pricing

import (
	"encoding/json"
	"strings"
	"testing"
)

// specMargins is the margin set the spec worked example and the Python tests use
// (team_pct 0.12, not the 0.11 default).
func specMargins() MarginAssumptions {
	return MarginAssumptions{AdSpendPct: 0.30, TeamPct: 0.12, OverheadPct: 0.05, TargetProfitPct: 0.15}
}

// --- design_cp (ports tests/test_design_cp.py) ---------------------------------

func TestComputeEffectiveMachineTime(t *testing.T) {
	got, err := ComputeEffectiveMachineTime(5.5, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.375 {
		t.Fatalf("effective machine time = %v, want 1.375", got)
	}
}

func TestComputeEffectiveMachineTimeRejectsBadInput(t *testing.T) {
	if _, err := ComputeEffectiveMachineTime(5.5, 0); err == nil {
		t.Fatal("expected error for units_per_bed < 1")
	}
	if _, err := ComputeEffectiveMachineTime(0, 4); err == nil {
		t.Fatal("expected error for batch_print_time_hr <= 0")
	}
}

func TestComputeDesignCP(t *testing.T) {
	m := SlicerMetrics{FilamentG: 60, PurgeG: 8, ElectricityKwh: 0.5, EffectiveMachineTimeHr: 1.4, UnitsPerBed: 1, PrintTimeHr: 1.4}
	b := ComputeDesignCP(m, DefaultCostAssumptions())

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"filament", b.Filament, 60.0},
		{"purge", b.Purge, 8.0},
		{"electricity", b.Electricity, 6.0},
		{"machine", b.Machine, 63.0},
		{"finishing", b.Finishing, 30.0},
		{"consumables", b.Consumables, 15.0},
		{"subtotal", b.Subtotal, 182.0},
		{"failure_provision", b.FailureProvision, 10.92},
		{"design_cp", b.DesignCP, 192.92},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// --- selling_price (ports tests/test_selling_price.py) -------------------------

func TestRemainingMargin(t *testing.T) {
	m := specMargins()
	if got := roundHalfEven(RemainingMargin(m, nil), 2); got != 0.38 {
		t.Errorf("normal margin = %v, want 0.38", got)
	}
	stress := StressAdSpendPct
	if got := roundHalfEven(RemainingMargin(m, &stress), 2); got != 0.28 {
		t.Errorf("stress margin = %v, want 0.28", got)
	}
}

func TestSnapUpToLadder(t *testing.T) {
	ladder := []int{999, 1199, 1299}
	if got := SnapUpToLadder(1078.9, ladder); got == nil || *got != 1199 {
		t.Errorf("snap(1078.9) = %v, want 1199", got)
	}
	if got := SnapUpToLadder(999, ladder); got == nil || *got != 999 {
		t.Errorf("snap(999) = %v, want 999", got)
	}
	if got := SnapUpToLadder(5000, ladder); got != nil {
		t.Errorf("snap(5000) = %v, want nil", got)
	}
}

func TestGenerateSellingPriceWorkedExample(t *testing.T) {
	fixed := FixedVariableCosts{Packaging: 90, Shipping: 25, RtoCod: 15, PaymentGateway: 40}
	res, err := GenerateSellingPrice(240, fixed, BrandGifting, specMargins(), nil, StressAdSpendPct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RawSP != 1078.95 {
		t.Errorf("raw_sp = %v, want 1078.95", res.RawSP)
	}
	if roundHalfEven(res.RawSP, 0) != 1079 {
		t.Errorf("round(raw_sp) = %v, want 1079", roundHalfEven(res.RawSP, 0))
	}
	if res.RecommendedSP == nil || *res.RecommendedSP != 1199 {
		t.Fatalf("recommended_sp = %v, want 1199", res.RecommendedSP)
	}
	if !res.PassesNormal {
		t.Error("passes_normal = false, want true")
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "210") {
		t.Errorf("warnings = %v, want a single ₹999 warning containing 210", res.Warnings)
	}
}

func TestGenerateSellingPriceAboveLadder(t *testing.T) {
	// A huge CP that no rung can cover: recommended is nil, every rung warned,
	// plus the top-of-ladder warning.
	res, err := GenerateSellingPrice(100000, FixedVariableCosts{}, BrandGifting, specMargins(), nil, StressAdSpendPct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RecommendedSP != nil {
		t.Errorf("recommended_sp = %v, want nil", res.RecommendedSP)
	}
	if res.PassesNormal {
		t.Error("passes_normal = true, want false")
	}
	last := res.Warnings[len(res.Warnings)-1]
	if !strings.Contains(last, "exceeds the top of the ladder") {
		t.Errorf("last warning = %q, want the top-of-ladder message", last)
	}
}

// --- status (ports tests/test_status.py) ---------------------------------------

func mustStatus(t *testing.T, designCP, targetSP float64, brand Brand, metrics *SlicerMetrics) DesignStatusResult {
	t.Helper()
	res, err := EvaluateStatus(designCP, targetSP, brand, metrics, nil, nil, GiftingEntryRung)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return res
}

func TestEvaluateStatusVerdicts(t *testing.T) {
	if r := mustStatus(t, 240, 999, BrandGifting, nil); r.Status != StatusGreen || r.CPPct != 0.2402 {
		t.Errorf("240/999 gifting = %v (cp %v), want green 0.2402", r.Status, r.CPPct)
	}
	if r := mustStatus(t, 280, 999, BrandGifting, nil); r.Status != StatusYellow {
		t.Errorf("280/999 gifting = %v, want yellow", r.Status)
	}
	if r := mustStatus(t, 350, 999, BrandGifting, nil); r.Status != StatusRed {
		t.Errorf("350/999 gifting = %v, want red", r.Status)
	}
	if r := mustStatus(t, 1150, 3999, BrandDecor, nil); r.Status != StatusGreen {
		t.Errorf("1150/3999 decor = %v, want green", r.Status)
	}
}

func TestEvaluateStatusMachineTimeDowngrade(t *testing.T) {
	metrics := &SlicerMetrics{EffectiveMachineTimeHr: 2.5, UnitsPerBed: 1, PrintTimeHr: 2.5, FilamentG: 60}
	r := mustStatus(t, 200, 999, BrandGifting, metrics)
	if r.Status != StatusYellow {
		t.Errorf("status = %v, want yellow (machine-time downgrade)", r.Status)
	}
	found := false
	for _, reason := range r.Reasons {
		if strings.Contains(strings.ToLower(reason), "machine time") {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %v, want one mentioning machine time", r.Reasons)
	}
}

func TestEvaluateStatusRejectsNonPositiveTarget(t *testing.T) {
	if _, err := EvaluateStatus(200, 0, BrandGifting, nil, nil, nil, GiftingEntryRung); err == nil {
		t.Fatal("expected error for target_sp <= 0")
	}
}

// --- JSON defaulting -----------------------------------------------------------

func TestSlicerMetricsDefaultsUnitsPerBed(t *testing.T) {
	var m SlicerMetrics
	if err := json.Unmarshal([]byte(`{"print_time_hr":1,"filament_g":60,"effective_machine_time_hr":1.4}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.UnitsPerBed != 1 {
		t.Errorf("units_per_bed = %d, want 1 (defaulted)", m.UnitsPerBed)
	}
}

func TestCostAssumptionsPartialKeepsDefaults(t *testing.T) {
	var c CostAssumptions
	if err := json.Unmarshal([]byte(`{"machine_hour_cost":50}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.MachineHourCost != 50 {
		t.Errorf("machine_hour_cost = %v, want 50", c.MachineHourCost)
	}
	if c.FilamentCostPerKg != 1000 || c.FailurePct != 0.06 {
		t.Errorf("defaults lost: filament=%v failure=%v", c.FilamentCostPerKg, c.FailurePct)
	}
}
