package httpapi

import (
	"context"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// Defaults for the estimate when the client omits sizing. Millimetres.
const (
	defaultTextHeightMM = 8.0
	defaultTextDepthMM  = 1.5
)

type personalisationEstimateRequest struct {
	Text     string  `json:"text"`
	HeightMM float64 `json:"height_mm"`
	DepthMM  float64 `json:"depth_mm"`
}

// personalisationEstimateResponse is a fast, pre-slice preview: how much a name
// adds to a product's material, time and cost. `source` is always "estimate" -
// the authoritative numbers come from the slice at production time.
type personalisationEstimateResponse struct {
	Source            string  `json:"source"`
	AddedGrams        float64 `json:"added_grams"`
	AddedTimeMinutes  float64 `json:"added_time_minutes"`
	BaseDesignCP      float64 `json:"base_design_cp"`
	EstimatedDesignCP float64 `json:"estimated_design_cp"`
	AddedDesignCP     float64 `json:"added_design_cp"`
}

// estimatePersonalisation returns a fast preview of what personalising a design
// costs, without slicing. It needs the design's base slice metrics; a design that
// has not been costed yet returns 409. Read-only (no DB writes) - it powers the
// live preview and never overwrites the authoritative slice metrics or pricing.
func (s *Server) estimatePersonalisation(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req personalisationEstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'text' field is required.")
		return
	}
	ctx := c.Request.Context()

	design, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	metrics, err := s.store.Q.GetLatestMetricsForDesign(ctx, id)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusConflict, "Cost this design first, then estimate personalisation.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the design's metrics.")
		return
	}

	spec := pricing.PersonalisationSpec{
		Text:     req.Text,
		HeightMM: floatOrDefault(req.HeightMM, defaultTextHeightMM),
		DepthMM:  floatOrDefault(req.DepthMM, defaultTextDepthMM),
	}
	base := baseMetricsFrom(metrics)
	cost := s.costAssumptionsForBrand(ctx, design.BrandSlug)

	estimated, delta := pricing.EstimatePersonalisation(
		base, spec, pricing.DensityForMaterial(design.Material), pricing.DefaultEstimationConfig())
	baseCP := pricing.ComputeDesignCP(base, cost).DesignCP
	estCP := pricing.ComputeDesignCP(estimated, cost).DesignCP

	c.JSON(http.StatusOK, personalisationEstimateResponse{
		Source:            "estimate",
		AddedGrams:        round2(delta.GramsAdded),
		AddedTimeMinutes:  round2(delta.TimeAddedHr * 60),
		BaseDesignCP:      round2(baseCP),
		EstimatedDesignCP: round2(estCP),
		AddedDesignCP:     round2(estCP - baseCP),
	})
}

func baseMetricsFrom(m gen.GetLatestMetricsForDesignRow) pricing.SlicerMetrics {
	return pricing.SlicerMetrics{
		PrintTimeHr:            m.PrintTimeHr,
		FilamentG:              m.FilamentG,
		PurgeG:                 m.PurgeG,
		SupportG:               m.SupportG,
		ColourChanges:          int(m.ColourChanges),
		ElectricityKwh:         m.ElectricityKwh,
		UnitsPerBed:            int(m.UnitsPerBed),
		EffectiveMachineTimeHr: m.EffectiveMachineTimeHr,
	}
}

// costAssumptionsForBrand loads the brand's default cost set, falling back to the
// engine defaults when the brand has none (matching the slice worker's path).
func (s *Server) costAssumptionsForBrand(ctx context.Context, brand string) pricing.CostAssumptions {
	row, err := s.store.Q.GetDefaultCostAssumptionForBrand(ctx, &brand)
	if err != nil {
		return pricing.DefaultCostAssumptions()
	}
	return pricing.CostAssumptions{
		FilamentCostPerKg:      row.FilamentCostPerKg,
		ElectricityCostPerUnit: row.ElectricityCostPerUnit,
		MachineHourCost:        row.MachineHourCost,
		FinishingLabour:        row.FinishingLabour,
		Consumables:            row.Consumables,
		FailurePct:             row.FailurePct,
	}
}

func floatOrDefault(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
