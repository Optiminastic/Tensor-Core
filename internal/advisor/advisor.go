// Package advisor builds the LLM request that turns a design's machine, slice
// metrics, pricing, and geometry into a nozzle-aware optimization report, and
// parses the structured result. It is pure: no DB, no HTTP, no globals beyond
// the prompt/schema constants. The handler gathers the Inputs and runs the call.
package advisor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/integrations/openrouter"
)

// toolName is the function the model must call; forcing it guarantees structured
// JSON matching the advice schema.
const toolName = "advise"

const systemPrompt = `You are a 3D-printing optimization advisor for an FDM print shop.
Given a design's geometry, slice metrics, pricing, and the EXACT printer nozzle it will run on,
recommend how to optimize the print.

Rules:
- Tailor every recommendation to the given nozzle diameter and flow (e.g. a 0.2mm nozzle suits fine
  detail but is slow; 0.6/0.8 or high_flow suits large solid parts).
- Recommend a filament. Prefer one from the machine's supported_filaments list when a suitable one
  exists, and name its exact filament_preset.
- Advise whether support is needed and the best support strategy and material.
- Give a ranked list of concrete, quantitative improvements across orientation, infill, walls, layer
  height, geometry, and cost. Prefer real numbers (grams, minutes, mm, rupees) when the inputs allow.
- Assign an overall verdict: green (good to print), yellow (prints but improvable), red (likely to
  fail or waste material).
Respond ONLY by calling the advise tool.`

// Filament is one entry of a machine's supported filaments.
type Filament struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
	IsDefault      bool    `json:"is_default"`
}

// Machine is the printer profile the design runs on - the nozzle is the key input.
type Machine struct {
	Name               string     `json:"name"`
	Family             string     `json:"family"`
	NozzleMM           float64    `json:"nozzle_mm"`
	Flow               string     `json:"flow"`
	LayerHeightMinMM   float64    `json:"layer_height_min_mm"`
	LayerHeightMaxMM   float64    `json:"layer_height_max_mm"`
	SupportedFilaments []Filament `json:"supported_filaments"`
}

// Metrics is the design's latest per-unit slice result.
type Metrics struct {
	FilamentG     float64 `json:"filament_g"`
	PurgeG        float64 `json:"purge_g"`
	SupportG      float64 `json:"support_g"`
	SupportUsed   bool    `json:"support_used"`
	WallLoops     int     `json:"wall_loops"`
	InfillPct     float64 `json:"infill_density_pct"`
	LayerHeightMM float64 `json:"layer_height_mm"`
	PrintTimeHr   float64 `json:"print_time_hr"`
	ColourChanges int     `json:"colour_changes"`
}

// Orientation is the advisory least-support recommendation, when present.
type Orientation struct {
	OverhangBaseline    float64 `json:"overhang_area_baseline"`
	OverhangRecommended float64 `json:"overhang_area_recommended"`
	EstReductionPct     float64 `json:"est_reduction_pct"`
	Description         string  `json:"description"`
	AlreadyOptimal      bool    `json:"already_optimal"`
}

// Geometry is a summary computed from the mesh, when the model is an STL.
type Geometry struct {
	BBoxMM        [3]float64 `json:"bbox_mm"`
	VolumeCM3     float64    `json:"volume_cm3"`
	TriangleCount int        `json:"triangle_count"`
}

// Pricing is the design's costing verdict.
type Pricing struct {
	DesignCP float64 `json:"design_cp"`
	Verdict  string  `json:"verdict"`
	CPPct    float64 `json:"cp_pct"`
}

// Specs are the design's submitted answers.
type Specs struct {
	Material    string `json:"material"`
	Quality     string `json:"quality"`
	UnitsPerBed int    `json:"units_per_bed"`
	Colour      string `json:"colour,omitempty"`
	Finish      string `json:"finish"`
}

// Inputs is everything the advisor reasons over for one design.
type Inputs struct {
	Specs       Specs        `json:"specs"`
	Machine     Machine      `json:"machine"`
	Metrics     Metrics      `json:"metrics"`
	Pricing     Pricing      `json:"pricing"`
	Geometry    *Geometry    `json:"geometry,omitempty"`
	Orientation *Orientation `json:"orientation,omitempty"`
}

// FilamentAdvice is the recommended filament and why.
type FilamentAdvice struct {
	Material          string `json:"material"`
	RecommendedPreset string `json:"recommended_preset"`
	Rationale         string `json:"rationale"`
}

// SupportAdvice is the support recommendation.
type SupportAdvice struct {
	Needed    bool   `json:"needed"`
	Strategy  string `json:"strategy"`
	Material  string `json:"material"`
	Rationale string `json:"rationale"`
}

// Recommendation is one ranked, cross-cutting improvement.
type Recommendation struct {
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Category string `json:"category"`
	Impact   string `json:"impact"`
}

// Advice is the full structured report the model returns.
type Advice struct {
	Verdict         string           `json:"verdict"`
	Summary         string           `json:"summary"`
	Filament        FilamentAdvice   `json:"filament"`
	Support         SupportAdvice    `json:"support"`
	Recommendations []Recommendation `json:"recommendations"`
}

// BuildRequest composes the OpenRouter request: the system prompt, the inputs as
// JSON, and the advice tool the model must fill.
func BuildRequest(in Inputs, model string) openrouter.Request {
	inputJSON, _ := json.MarshalIndent(in, "", "  ")
	return openrouter.Request{
		Model:  model,
		System: systemPrompt,
		User:   "Optimize this design. Call the advise tool with your report.\n\n" + string(inputJSON),
		Tool: openrouter.Tool{
			Name:        toolName,
			Description: "Return the nozzle-aware optimization report for the design.",
			Parameters:  adviceSchema(),
		},
	}
}

// Parse decodes the tool arguments into Advice and normalises the verdict.
func Parse(raw json.RawMessage) (Advice, error) {
	var a Advice
	if err := json.Unmarshal(raw, &a); err != nil {
		return Advice{}, fmt.Errorf("advisor: decode advice: %w", err)
	}
	a.Verdict = strings.ToLower(strings.TrimSpace(a.Verdict))
	switch a.Verdict {
	case "green", "yellow", "red":
	default:
		a.Verdict = "yellow"
	}
	return a, nil
}

// adviceSchema is the JSON Schema the model's tool arguments must satisfy.
func adviceSchema() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"verdict", "summary", "filament", "support", "recommendations"},
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"green", "yellow", "red"}},
			"summary": str,
			"filament": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"material", "recommended_preset", "rationale"},
				"properties": map[string]any{
					"material":           str,
					"recommended_preset": str,
					"rationale":          str,
				},
			},
			"support": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"needed", "strategy", "material", "rationale"},
				"properties": map[string]any{
					"needed":    map[string]any{"type": "boolean"},
					"strategy":  str,
					"material":  str,
					"rationale": str,
				},
			},
			"recommendations": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"title", "detail", "category", "impact"},
					"properties": map[string]any{
						"title":  str,
						"detail": str,
						"category": map[string]any{"type": "string", "enum": []string{
							"orientation", "filament", "support", "infill",
							"walls", "layer_height", "geometry", "cost",
						}},
						"impact": str,
					},
				},
			},
		},
	}
}
