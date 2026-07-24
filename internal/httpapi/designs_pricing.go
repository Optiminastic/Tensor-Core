package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/brandpolicy"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/designadvice"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// sliceMetricsInput is the per-unit metrics the worker reports. It maps onto the
// engine's SlicerMetrics for costing, and carries a few extra facts used only for
// suggestions (infill, walls, support).
type sliceMetricsInput struct {
	PrintTimeHr            float64 `json:"print_time_hr" binding:"gt=0"`
	EffectiveMachineTimeHr float64 `json:"effective_machine_time_hr" binding:"gt=0"`
	FilamentG              float64 `json:"filament_g" binding:"gte=0"`
	UnitsPerBed            int     `json:"units_per_bed" binding:"gte=1"`
	PurgeG                 float64 `json:"purge_g"`
	SupportG               float64 `json:"support_g"`
	ColourChanges          int     `json:"colour_changes"`
	ElectricityKwh         float64 `json:"electricity_kwh"`
	LayerHeightMm          float64 `json:"layer_height_mm"`
	InfillDensityPct       float64 `json:"infill_density_pct"`
	WallLoops              int     `json:"wall_loops"`
	SupportUsed            bool    `json:"support_used"`
	FilamentLengthMm       float64 `json:"filament_length_mm"`
	GcodeKey               string  `json:"gcode_key"`
}

func (in sliceMetricsInput) toSlicerMetrics() pricing.SlicerMetrics {
	return pricing.SlicerMetrics{
		PrintTimeHr: in.PrintTimeHr, FilamentG: in.FilamentG, PurgeG: in.PurgeG,
		SupportG: in.SupportG, ColourChanges: in.ColourChanges, ElectricityKwh: in.ElectricityKwh,
		UnitsPerBed: in.UnitsPerBed, EffectiveMachineTimeHr: in.EffectiveMachineTimeHr,
	}
}

func (in sliceMetricsInput) toInsertParams(jobID uuid.UUID) gen.InsertSliceMetricsParams {
	return gen.InsertSliceMetricsParams{
		JobID: jobID, PrintTimeHr: in.PrintTimeHr, EffectiveMachineTimeHr: in.EffectiveMachineTimeHr,
		FilamentG: in.FilamentG, PurgeG: in.PurgeG, SupportG: in.SupportG,
		ColourChanges: int32(in.ColourChanges), ElectricityKwh: in.ElectricityKwh,
		UnitsPerBed: int32(in.UnitsPerBed), LayerHeightMm: in.LayerHeightMm,
		InfillDensityPct: in.InfillDensityPct, WallLoops: int32(in.WallLoops),
		SupportUsed: in.SupportUsed, FilamentLengthMm: in.FilamentLengthMm, GcodeKey: in.GcodeKey,
	}
}

// verdict bundles the classification of a design against its brand's policy.
type verdict struct {
	status      string
	cpPct       float64
	recommended *int
	reasons     []string
}

// priceDesign runs the cost engine on the slice metrics, classifies the design
// against the brand's policy, computes fix suggestions, and persists the result.
// Cost assumptions use the engine defaults for now (wiring the brand's cost set
// is a follow-up). It sets the design to "priced" on success.
func (s *Server) priceDesign(ctx context.Context, designID uuid.UUID, brandSlug string, in sliceMetricsInput) error {
	metrics := in.toSlicerMetrics()
	breakdown := pricing.ComputeDesignCP(metrics, pricing.DefaultCostAssumptions())
	brand := pricing.Brand(brandSlug)

	policy, err := brandpolicy.Load(ctx, s.store.Q, brand)
	if err != nil {
		return err
	}
	v, err := s.classify(brand, breakdown.DesignCP, metrics, policy)
	if err != nil {
		return err
	}

	suggestions := designadvice.Suggest(designadvice.Input{
		Verdict: v.status, FilamentG: in.FilamentG, EffectiveMachineTimeHr: in.EffectiveMachineTimeHr,
		InfillPct: in.InfillDensityPct, SupportUsed: in.SupportUsed, UnitsPerBed: in.UnitsPerBed,
		EntryMachineHours: entryHoursOf(policy),
	})

	return s.persistPricing(ctx, designID, breakdown, v, suggestions)
}

// classify turns a Design CP into a Green/Yellow/Red verdict by first finding the
// recommended ladder price, then measuring CP against it. A CP too high for even
// the top rung is Red outright.
func (s *Server) classify(
	brand pricing.Brand, designCP float64, metrics pricing.SlicerMetrics, policy *brandpolicy.Policy,
) (verdict, error) {
	var ladder []int
	if policy != nil {
		ladder = policy.Ladder
	}
	selling, err := pricing.GenerateSellingPrice(
		designCP, pricing.FixedVariableCosts{}, brand, pricing.DefaultMarginAssumptions(),
		ladder, pricing.StressAdSpendPct,
	)
	if err != nil {
		return verdict{}, err
	}
	if selling.RecommendedSP == nil {
		return verdict{
			status: "red", cpPct: 0,
			reasons: []string{"Design CP is too high for the brand's price ladder; even the top rung cannot hold a safe margin."},
		}, nil
	}

	status, err := pricing.EvaluateStatus(
		designCP, float64(*selling.RecommendedSP), brand, &metrics,
		thresholdsOf(policy), entryHoursOf(policy), entryRungOf(policy),
	)
	if err != nil {
		return verdict{}, err
	}
	return verdict{
		status: string(status.Status), cpPct: status.CPPct,
		recommended: selling.RecommendedSP, reasons: status.Reasons,
	}, nil
}

func (s *Server) persistPricing(
	ctx context.Context, designID uuid.UUID, breakdown pricing.DesignCPBreakdown, v verdict, suggestions []string,
) error {
	breakdownJSON, err := json.Marshal(breakdown)
	if err != nil {
		return err
	}
	reasonsJSON, err := json.Marshal(v.reasons)
	if err != nil {
		return err
	}
	suggestionsJSON, err := json.Marshal(suggestions)
	if err != nil {
		return err
	}
	var recommended *int32
	if v.recommended != nil {
		r := int32(*v.recommended)
		recommended = &r
	}

	return s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.UpsertDesignPricing(ctx, gen.UpsertDesignPricingParams{
			DesignID: designID, DesignCp: breakdown.DesignCP, Breakdown: breakdownJSON,
			Verdict: v.status, CpPct: v.cpPct, RecommendedSp: recommended,
			Reasons: reasonsJSON, Suggestions: suggestionsJSON,
		}); err != nil {
			return err
		}
		return q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designPriced, ID: designID})
	})
}

// failJob marks a job failed with a reason and drops its design to "failed", so a
// design never sits stuck when the slice cannot proceed. Best effort.
func (s *Server) failJob(ctx context.Context, jobID uuid.UUID, reason string) {
	msg := reason
	_ = s.store.Q.UpdateSliceJobStatus(ctx, gen.UpdateSliceJobStatusParams{
		Status: jobFailed, Error: &msg, ID: jobID,
	})
	job, err := s.store.Q.GetSliceJobByID(ctx, jobID)
	if err != nil {
		return
	}
	_ = s.store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designFailed, ID: job.DesignID})
}

// Nil-safe accessors so pricing works whether or not the brand has a policy row.

func thresholdsOf(p *brandpolicy.Policy) *[2]float64 {
	if p == nil {
		return nil
	}
	return &[2]float64{p.GreenMax, p.YellowMax}
}

func entryHoursOf(p *brandpolicy.Policy) *float64 {
	if p == nil {
		return nil
	}
	return p.EntryMachineHours
}

func entryRungOf(p *brandpolicy.Policy) int {
	if p == nil || p.EntryRung == nil {
		return pricing.GiftingEntryRung
	}
	return *p.EntryRung
}
