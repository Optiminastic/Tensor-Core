package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/advisor"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// advisorReady fails closed when the AI advisor cannot run: no object store (it
// needs the STL for a geometry summary) or no OpenRouter key configured.
func (s *Server) advisorReady(c *gin.Context) bool {
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "The design pipeline is not configured.")
		return false
	}
	if !s.cfg.AdvisorConfigured() || s.openrouter == nil {
		detail(c, http.StatusServiceUnavailable, "AI optimization is not configured.")
		return false
	}
	return true
}

// optimizeDesign runs the nozzle-aware AI advisor for a costed design and returns a
// structured report (verdict, filament, support, ranked recommendations). The
// result is cached per (design, input signature), so a repeat request for the same
// inputs is free; a re-slice changes the metrics and yields fresh advice.
func (s *Server) optimizeDesign(c *gin.Context) {
	if !s.advisorReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	d, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	metrics, err := s.latestMetrics(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	if metrics == nil {
		detail(c, http.StatusConflict, "Slice the design first, then run optimization.")
		return
	}
	pricing, err := s.designPricing(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	machine, err := s.designMachine(ctx, d.MachineID)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	inputs := s.buildAdvisorInputs(ctx, d, machine, metrics, pricing)
	hash := hashInputs(inputs, s.cfg.OpenRouterModel)

	// Cache hit: return the stored report verbatim.
	if cached, err := s.store.Q.GetDesignOptimization(ctx, gen.GetDesignOptimizationParams{DesignID: id, InputHash: hash}); err == nil {
		c.Data(http.StatusOK, "application/json; charset=utf-8", cached.Result)
		return
	} else if !isNoRows(err) {
		dbError(c, err, "", "Could not read the optimization cache.")
		return
	}

	// Miss: call the model, parse, cache, return.
	raw, err := s.openrouter.Complete(ctx, s.cfg.OpenRouterAPIKey, advisor.BuildRequest(inputs, s.cfg.OpenRouterModel))
	if err != nil {
		s.logger.Error("advisor call failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, "The AI optimizer could not complete. Please try again.")
		return
	}
	advice, err := advisor.Parse(raw)
	if err != nil {
		s.logger.Error("advisor parse failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, "The AI optimizer returned an unexpected result.")
		return
	}
	result, err := json.Marshal(advice)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the optimization result.")
		return
	}
	if _, err := s.store.Q.UpsertDesignOptimization(ctx, gen.UpsertDesignOptimizationParams{
		ID: uuid.New(), DesignID: id, InputHash: hash, Result: result, Model: s.cfg.OpenRouterModel,
	}); err != nil {
		// Non-fatal: the advice is valid even if the cache write failed.
		s.logger.Warn("optimization cache write failed", "design", id, "error", err)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", result)
}

// buildAdvisorInputs assembles everything the advisor reasons over from the design
// row, its resolved machine, its latest metrics, and its pricing.
func (s *Server) buildAdvisorInputs(
	ctx context.Context, d gen.GetDesignByIDRow, machine *machineConfigResponse,
	metrics *metricsResponse, pricing *pricingResponse,
) advisor.Inputs {
	in := advisor.Inputs{
		Specs: advisor.Specs{
			Material: d.Material, Quality: d.Quality, UnitsPerBed: int(d.UnitsPerBed),
			Colour: strVal(d.Colour), Finish: d.Finish,
		},
		Machine: advisorMachine(machine, d.Material),
		Metrics: advisor.Metrics{
			FilamentG: metrics.FilamentG, PurgeG: metrics.PurgeG, SupportG: metrics.SupportG,
			SupportUsed: metrics.SupportUsed, WallLoops: metrics.WallLoops,
			InfillPct: metrics.InfillDensityPct, LayerHeightMM: metrics.LayerHeightMm,
			PrintTimeHr: metrics.PrintTimeHr, ColourChanges: metrics.ColourChanges,
		},
	}
	if pricing != nil {
		in.Pricing = advisor.Pricing{DesignCP: pricing.DesignCP, Verdict: pricing.Verdict, CPPct: pricing.CPPct}
	}
	in.Orientation = parseOrientation(metrics.Orientation)
	in.Geometry = s.geometrySummary(ctx, d.StlKey)
	return in
}

// advisorMachine maps the resolved machine config to the advisor's machine, or a
// built-in default (H2S 0.4) when the design has no machine linked.
func advisorMachine(m *machineConfigResponse, material string) advisor.Machine {
	if m == nil {
		return defaultMachine(material)
	}
	fils := make([]advisor.Filament, len(m.SupportedFilaments))
	for i, f := range m.SupportedFilaments {
		fils[i] = advisor.Filament{Material: f.Material, FilamentPreset: f.FilamentPreset, Density: f.Density, IsDefault: f.IsDefault}
	}
	return advisor.Machine{
		Name: m.Name, Family: m.Family, NozzleMM: m.NozzleMM, Flow: m.Flow,
		LayerHeightMinMM: m.LayerHeightMinMM, LayerHeightMaxMM: m.LayerHeightMaxMM,
		SupportedFilaments: fils,
	}
}

func defaultMachine(material string) advisor.Machine {
	return advisor.Machine{
		Name: "Default H2S 0.4", Family: "H2S", NozzleMM: 0.4, Flow: "standard",
		LayerHeightMinMM: 0.08, LayerHeightMaxMM: 0.28,
		SupportedFilaments: []advisor.Filament{defaultFilament(material)},
	}
}

// defaultFilament mirrors internal/slicing/profiles.go's material -> H2S preset map
// so a machineless design still shows the advisor a concrete filament.
func defaultFilament(material string) advisor.Filament {
	switch strings.ToUpper(strings.TrimSpace(material)) {
	case "PETG":
		return advisor.Filament{Material: "PETG", FilamentPreset: "Bambu PETG Basic @BBL H2S", Density: 1.27, IsDefault: true}
	case "ABS":
		return advisor.Filament{Material: "ABS", FilamentPreset: "Bambu ABS @BBL H2S", Density: 1.04, IsDefault: true}
	default:
		return advisor.Filament{Material: "PLA", FilamentPreset: "Bambu PLA Basic @BBL H2S", Density: 1.24, IsDefault: true}
	}
}

// geometrySummary loads the STL and computes a bbox/volume/triangle summary. It is
// best-effort: a missing store object, a non-STL model, or a parse error yields nil
// and the advisor simply reasons without geometry.
func (s *Server) geometrySummary(ctx context.Context, stlKey string) *advisor.Geometry {
	if stlKey == "" || strings.ToLower(filepath.Ext(stlKey)) != ".stl" {
		return nil
	}
	obj, err := s.storage.Get(ctx, stlKey)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if err != nil {
		return nil
	}
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		return nil
	}
	return &advisor.Geometry{
		BBoxMM: [3]float64{
			round2(mesh.Max.X - mesh.Min.X),
			round2(mesh.Max.Y - mesh.Min.Y),
			round2(mesh.Max.Z - mesh.Min.Z),
		},
		VolumeCM3:     round2(meshVolumeCM3(mesh)),
		TriangleCount: len(mesh.Triangles),
	}
}

// meshVolumeCM3 is the enclosed volume via the signed-tetrahedron sum, in cm3. A
// non-closed mesh yields an approximate value; that is fine for an advisory hint.
func meshVolumeCM3(m orientation.Mesh) float64 {
	var v6 float64
	for _, t := range m.Triangles {
		v6 += t.V0.X*(t.V1.Y*t.V2.Z-t.V1.Z*t.V2.Y) -
			t.V0.Y*(t.V1.X*t.V2.Z-t.V1.Z*t.V2.X) +
			t.V0.Z*(t.V1.X*t.V2.Y-t.V1.Y*t.V2.X)
	}
	return math.Abs(v6) / 6.0 / 1000.0 // mm3 -> cm3
}

// parseOrientation decodes the stored orientation recommendation, or nil.
func parseOrientation(raw json.RawMessage) *advisor.Orientation {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var o advisor.Orientation
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil
	}
	return &o
}

// hashInputs is the deterministic cache key for one advice request: the inputs plus
// the model, so a re-slice or a model change both produce fresh advice.
func hashInputs(in advisor.Inputs, model string) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(append(b, []byte("|"+model)...))
	return hex.EncodeToString(sum[:])
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
