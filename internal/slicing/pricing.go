package slicing

// The slice -> price domain logic, moved out of the HTTP layer so the in-process
// Go worker can call it directly (replacing the old POST /internal/slice-result
// callback). All functions are ctx + store only; no gin.Context.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/brandpolicy"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/designadvice"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// Lifecycle statuses (the same free-text values the HTTP layer uses).
const (
	jobDone       = "done"
	jobFailed     = "failed"
	designSlicing = "slicing"
	designPriced  = "priced"
	designFailed  = "failed"
)

func (m PerUnitMetrics) toSlicerMetrics() pricing.SlicerMetrics {
	return pricing.SlicerMetrics{
		PrintTimeHr: m.PrintTimeHr, FilamentG: m.FilamentG, PurgeG: m.PurgeG,
		SupportG: m.SupportG, ColourChanges: m.ColourChanges, ElectricityKwh: m.ElectricityKwh,
		UnitsPerBed: m.UnitsPerBed, EffectiveMachineTimeHr: m.EffectiveMachineTimeHr,
	}
}

func (m PerUnitMetrics) toInsertParams(jobID uuid.UUID) gen.InsertSliceMetricsParams {
	return gen.InsertSliceMetricsParams{
		JobID: jobID, PrintTimeHr: m.PrintTimeHr, EffectiveMachineTimeHr: m.EffectiveMachineTimeHr,
		FilamentG: m.FilamentG, PurgeG: m.PurgeG, SupportG: m.SupportG,
		ColourChanges: int32(m.ColourChanges), ElectricityKwh: m.ElectricityKwh,
		UnitsPerBed: int32(m.UnitsPerBed), LayerHeightMm: m.LayerHeightMm,
		InfillDensityPct: m.InfillDensityPct, WallLoops: int32(m.WallLoops),
		SupportUsed: m.SupportUsed, FilamentLengthMm: m.FilamentLengthMm, GcodeKey: m.GcodeKey,
	}
}

// MarkSlicing flips a design to "slicing" when the worker picks up its job.
func MarkSlicing(ctx context.Context, store *db.Store, designID uuid.UUID) error {
	return store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designSlicing, ID: designID})
}

// ProcessSliceResult persists the metrics, closes the slice job, and prices the
// design. The in-process replacement for recordSlice + the slice-result callback.
//
// All of it runs in a single transaction: the metrics insert, the job close, and
// the pricing writes commit together or not at all, so a mid-sequence failure can
// never leave a design with stored metrics but an unclosed job or no pricing.
func ProcessSliceResult(
	ctx context.Context, store *db.Store, jobID, designID uuid.UUID, brandSlug string, m PerUnitMetrics,
) error {
	return store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.InsertSliceMetrics(ctx, m.toInsertParams(jobID)); err != nil {
			return err
		}
		if err := q.UpdateSliceJobStatus(ctx, gen.UpdateSliceJobStatusParams{
			Status: jobDone, Error: nil, ID: jobID,
		}); err != nil {
			return err
		}
		return priceDesign(ctx, q, designID, brandSlug, m)
	})
}

// FailJob marks a job failed with a reason and drops its design to "failed", so a
// design never sits stuck when the slice cannot proceed. Best effort.
func FailJob(ctx context.Context, store *db.Store, jobID uuid.UUID, reason string) {
	msg := reason
	_ = store.Q.UpdateSliceJobStatus(ctx, gen.UpdateSliceJobStatusParams{
		Status: jobFailed, Error: &msg, ID: jobID,
	})
	job, err := store.Q.GetSliceJobByID(ctx, jobID)
	if err != nil {
		return
	}
	_ = store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designFailed, ID: job.DesignID})
}

type verdict struct {
	status      string
	cpPct       float64
	recommended *int
	reasons     []string
}

type pricingInputs struct {
	cost    pricing.CostAssumptions
	fixed   pricing.FixedVariableCosts
	margins pricing.MarginAssumptions
}

// loadPricingInputs reads the brand's default cost assumption set (its own, else
// the global default), falling back to the engine defaults when none is set.
func loadPricingInputs(ctx context.Context, q *gen.Queries, brandSlug string) (pricingInputs, error) {
	slug := brandSlug
	row, err := q.GetDefaultCostAssumptionForBrand(ctx, &slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pricingInputs{
				cost:    pricing.DefaultCostAssumptions(),
				fixed:   pricing.FixedVariableCosts{},
				margins: pricing.DefaultMarginAssumptions(),
			}, nil
		}
		return pricingInputs{}, err
	}
	var fixed pricing.FixedVariableCosts
	if err := json.Unmarshal(nonEmptyJSON(row.FixedCosts), &fixed); err != nil {
		return pricingInputs{}, err
	}
	var margins pricing.MarginAssumptions
	if err := json.Unmarshal(nonEmptyJSON(row.Margins), &margins); err != nil {
		return pricingInputs{}, err
	}
	return pricingInputs{
		cost: pricing.CostAssumptions{
			FilamentCostPerKg:      row.FilamentCostPerKg,
			ElectricityCostPerUnit: row.ElectricityCostPerUnit,
			MachineHourCost:        row.MachineHourCost,
			FinishingLabour:        row.FinishingLabour,
			Consumables:            row.Consumables,
			FailurePct:             row.FailurePct,
		},
		fixed:   fixed,
		margins: margins,
	}, nil
}

func nonEmptyJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

func priceDesign(ctx context.Context, q *gen.Queries, designID uuid.UUID, brandSlug string, in PerUnitMetrics) error {
	inputs, err := loadPricingInputs(ctx, q, brandSlug)
	if err != nil {
		return err
	}
	metrics := in.toSlicerMetrics()
	breakdown := pricing.ComputeDesignCP(metrics, inputs.cost)
	brand := pricing.Brand(brandSlug)

	policy, err := brandpolicy.Load(ctx, q, brand)
	if err != nil {
		return err
	}
	v, selling, err := classify(brand, breakdown.DesignCP, metrics, policy, inputs)
	if err != nil {
		return err
	}

	suggestions := designadvice.Suggest(designadvice.Input{
		Verdict: v.status, FilamentG: in.FilamentG, EffectiveMachineTimeHr: in.EffectiveMachineTimeHr,
		InfillPct: in.InfillDensityPct, SupportUsed: in.SupportUsed, UnitsPerBed: in.UnitsPerBed,
		EntryMachineHours: entryHoursOf(policy),
	})

	return persistPricing(ctx, q, designID, breakdown, v, selling, suggestions)
}

func classify(
	brand pricing.Brand, designCP float64, metrics pricing.SlicerMetrics, policy *brandpolicy.Policy,
	inputs pricingInputs,
) (verdict, pricing.SellingPriceResult, error) {
	var ladder []int
	if policy != nil {
		ladder = policy.Ladder
	}
	selling, err := pricing.GenerateSellingPrice(
		designCP, inputs.fixed, brand, inputs.margins, ladder, pricing.StressAdSpendPct,
	)
	if err != nil {
		return verdict{}, pricing.SellingPriceResult{}, err
	}
	if selling.RecommendedSP == nil {
		return verdict{
			status: "red", cpPct: 0,
			reasons: []string{"Design CP is too high for the brand's price ladder; even the top rung cannot hold a safe margin."},
		}, selling, nil
	}

	status, err := pricing.EvaluateStatus(
		designCP, float64(*selling.RecommendedSP), brand, &metrics,
		thresholdsOf(policy), entryHoursOf(policy), entryRungOf(policy),
	)
	if err != nil {
		return verdict{}, pricing.SellingPriceResult{}, err
	}
	return verdict{
		status: string(status.Status), cpPct: status.CPPct,
		recommended: selling.RecommendedSP, reasons: status.Reasons,
	}, selling, nil
}

// persistPricing writes the pricing row and flips the design to "priced". It runs
// on the caller's tx-bound querier (ProcessSliceResult's transaction), so the two
// writes are atomic with the metrics insert and job close that precede them.
func persistPricing(
	ctx context.Context, q *gen.Queries, designID uuid.UUID, breakdown pricing.DesignCPBreakdown,
	v verdict, selling pricing.SellingPriceResult, suggestions []string,
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
	warningsJSON, err := json.Marshal(selling.Warnings)
	if err != nil {
		return err
	}
	var recommended *int32
	if v.recommended != nil {
		r := int32(*v.recommended)
		recommended = &r
	}

	if err := q.UpsertDesignPricing(ctx, gen.UpsertDesignPricingParams{
		DesignID: designID, DesignCp: breakdown.DesignCP, Breakdown: breakdownJSON,
		Verdict: v.status, CpPct: v.cpPct, RecommendedSp: recommended,
		RawSp: selling.RawSP, CpPctAtRecommended: selling.CPPctAtRecommended,
		PassesNormal: selling.PassesNormal, SurvivesStress: selling.SurvivesStress,
		SpWarnings: warningsJSON, Reasons: reasonsJSON, Suggestions: suggestionsJSON,
	}); err != nil {
		return err
	}
	return q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designPriced, ID: designID})
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
