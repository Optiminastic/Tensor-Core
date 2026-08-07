package advisor

import (
	"strings"
	"testing"
)

func sampleInputs() Inputs {
	return Inputs{
		Specs:   Specs{Material: "PLA", Quality: "standard", UnitsPerBed: 1, Finish: "none"},
		Machine: Machine{Name: "H2S-A", Family: "H2S", NozzleMM: 0.4, Flow: "standard", SupportedFilaments: []Filament{{Material: "PLA", FilamentPreset: "Bambu PLA Basic @BBL H2S", Density: 1.24, IsDefault: true}}},
		Metrics: Metrics{FilamentG: 42, SupportG: 6, SupportUsed: true, WallLoops: 3, InfillPct: 15, LayerHeightMM: 0.2, PrintTimeHr: 2.1},
		Pricing: Pricing{DesignCP: 180, Verdict: "yellow", CPPct: 0.28},
	}
}

func TestBuildRequestIncludesNozzleAndFilament(t *testing.T) {
	req := BuildRequest(sampleInputs(), "anthropic/claude-3.7-sonnet")

	if req.Model != "anthropic/claude-3.7-sonnet" {
		t.Errorf("model = %q", req.Model)
	}
	if req.Tool.Name != toolName {
		t.Errorf("tool name = %q, want %q", req.Tool.Name, toolName)
	}
	// The nozzle and a supported filament preset must reach the model.
	for _, want := range []string{`"nozzle_mm": 0.4`, "Bambu PLA Basic @BBL H2S", `"flow": "standard"`} {
		if !strings.Contains(req.User, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
	// The schema forces the top-level advice fields.
	props, ok := req.Tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	for _, key := range []string{"verdict", "summary", "filament", "support", "recommendations"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing property %q", key)
		}
	}
}

func TestParseRoundTrips(t *testing.T) {
	raw := []byte(`{
		"verdict": "GREEN",
		"summary": "Solid, prints well.",
		"filament": {"material": "PLA", "recommended_preset": "Bambu PLA Basic @BBL H2S", "rationale": "matches nozzle"},
		"support": {"needed": true, "strategy": "tree", "material": "same", "rationale": "one overhang"},
		"recommendations": [
			{"title": "Reorient", "detail": "Lay flat", "category": "orientation", "impact": "-2000 mm2 overhang"}
		]
	}`)

	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Verdict != "green" {
		t.Errorf("verdict = %q, want normalized 'green'", a.Verdict)
	}
	if a.Filament.RecommendedPreset != "Bambu PLA Basic @BBL H2S" {
		t.Errorf("filament preset = %q", a.Filament.RecommendedPreset)
	}
	if !a.Support.Needed || a.Support.Strategy != "tree" {
		t.Errorf("support = %+v", a.Support)
	}
	if len(a.Recommendations) != 1 || a.Recommendations[0].Category != "orientation" {
		t.Errorf("recommendations = %+v", a.Recommendations)
	}
}

func TestParseDefaultsUnknownVerdict(t *testing.T) {
	a, err := Parse([]byte(`{"verdict":"maybe","summary":"x","filament":{},"support":{},"recommendations":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Verdict != "yellow" {
		t.Errorf("unknown verdict should default to yellow, got %q", a.Verdict)
	}
}
