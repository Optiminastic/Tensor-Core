package pricing

import "encoding/json"

// Brand is the fixed two-value key set the whole product is built around.
type Brand string

const (
	BrandGifting Brand = "gifting"
	BrandDecor   Brand = "decor"
)

// DesignStatus is the Green / Yellow / Red verdict of the pre-check.
type DesignStatus string

const (
	StatusGreen  DesignStatus = "green"
	StatusYellow DesignStatus = "yellow"
	StatusRed    DesignStatus = "red"
)

// SlicerMetrics is the per-unit output of the slicer plus the batching factor.
//
// print_time_hr, support_g, colour_changes and failure_risk are carried and
// validated but not used in any computation -- they exist for the record and
// for future rules. binding tags drive the HTTP-layer 422 validation; the
// engine itself trusts its inputs.
type SlicerMetrics struct {
	PrintTimeHr            float64 `json:"print_time_hr" binding:"gt=0"`
	FilamentG              float64 `json:"filament_g" binding:"gte=0"`
	PurgeG                 float64 `json:"purge_g" binding:"gte=0"`
	SupportG               float64 `json:"support_g" binding:"gte=0"`
	ColourChanges          int     `json:"colour_changes" binding:"gte=0"`
	ElectricityKwh         float64 `json:"electricity_kwh" binding:"gte=0"`
	UnitsPerBed            int     `json:"units_per_bed" binding:"gte=1"`
	EffectiveMachineTimeHr float64 `json:"effective_machine_time_hr" binding:"gt=0"`
	FailureRisk            float64 `json:"failure_risk" binding:"gte=0,lte=1"`
}

// UnmarshalJSON seeds the one non-zero default (units_per_bed = 1) before
// decoding, so an omitted units_per_bed becomes 1 -- matching Pydantic -- while
// an explicit 0 still decodes to 0 and fails validation, exactly as Python's
// ge=1 constraint does.
func (m *SlicerMetrics) UnmarshalJSON(b []byte) error {
	type alias SlicerMetrics
	seeded := alias{UnitsPerBed: 1}
	if err := json.Unmarshal(b, &seeded); err != nil {
		return err
	}
	*m = SlicerMetrics(seeded)
	return nil
}

// CostAssumptions are the cost inputs the Design CP is built from. All fields
// have non-zero defaults, applied on decode so an omitted "assumptions" object
// reproduces the Python engine defaults.
type CostAssumptions struct {
	FilamentCostPerKg      float64 `json:"filament_cost_per_kg"`
	ElectricityCostPerUnit float64 `json:"electricity_cost_per_unit"`
	MachineHourCost        float64 `json:"machine_hour_cost"`
	FinishingLabour        float64 `json:"finishing_labour"`
	Consumables            float64 `json:"consumables"`
	FailurePct             float64 `json:"failure_pct"`
}

// DefaultCostAssumptions mirrors CostAssumptions' Pydantic defaults.
func DefaultCostAssumptions() CostAssumptions {
	return CostAssumptions{
		FilamentCostPerKg:      1000.0,
		ElectricityCostPerUnit: 12.0,
		MachineHourCost:        45.0,
		FinishingLabour:        30.0,
		Consumables:            15.0,
		FailurePct:             0.06,
	}
}

// UnmarshalJSON applies the defaults first, then overlays whatever the client
// sent, so partial objects keep the defaulted fields.
func (c *CostAssumptions) UnmarshalJSON(b []byte) error {
	type alias CostAssumptions
	seeded := alias(DefaultCostAssumptions())
	if err := json.Unmarshal(b, &seeded); err != nil {
		return err
	}
	*c = CostAssumptions(seeded)
	return nil
}

// MarginAssumptions are the margin components subtracted from 1 to leave the
// share of price available for CP plus other fixed costs.
type MarginAssumptions struct {
	AdSpendPct      float64 `json:"ad_spend_pct"`
	TeamPct         float64 `json:"team_pct"`
	OverheadPct     float64 `json:"overhead_pct"`
	TargetProfitPct float64 `json:"target_profit_pct"`
}

// DefaultMarginAssumptions mirrors MarginAssumptions' Pydantic defaults. Note
// team_pct defaults to 0.11 here; the spec worked example and every test pass
// 0.12 explicitly, so 0.11 only ever applies when "margins" is omitted entirely.
func DefaultMarginAssumptions() MarginAssumptions {
	return MarginAssumptions{
		AdSpendPct:      0.30,
		TeamPct:         0.11,
		OverheadPct:     0.05,
		TargetProfitPct: 0.15,
	}
}

// UnmarshalJSON applies the defaults first, then overlays the client's values.
func (m *MarginAssumptions) UnmarshalJSON(b []byte) error {
	type alias MarginAssumptions
	seeded := alias(DefaultMarginAssumptions())
	if err := json.Unmarshal(b, &seeded); err != nil {
		return err
	}
	*m = MarginAssumptions(seeded)
	return nil
}

// FixedVariableCosts are the per-unit fixed and variable costs other than CP.
// Every field defaults to 0, which is the Go zero value, so no custom decode is
// needed.
type FixedVariableCosts struct {
	Packaging      float64 `json:"packaging"`
	Shipping       float64 `json:"shipping"`
	RtoCod         float64 `json:"rto_cod"`
	PaymentGateway float64 `json:"payment_gateway"`
	TechAllocation float64 `json:"tech_allocation"`
	Other          float64 `json:"other"`
}

// TotalExcludingCP is the rounded sum of every fixed/variable cost except CP.
func (f FixedVariableCosts) TotalExcludingCP() float64 {
	return roundHalfEven(
		f.Packaging+f.Shipping+f.RtoCod+f.PaymentGateway+f.TechAllocation+f.Other, 2,
	)
}

// DesignCPBreakdown is the itemised Design CP, every field rounded to 2 dp.
type DesignCPBreakdown struct {
	Filament         float64 `json:"filament"`
	Purge            float64 `json:"purge"`
	Electricity      float64 `json:"electricity"`
	Machine          float64 `json:"machine"`
	Finishing        float64 `json:"finishing"`
	Consumables      float64 `json:"consumables"`
	FailureProvision float64 `json:"failure_provision"`
	Subtotal         float64 `json:"subtotal"`
	DesignCP         float64 `json:"design_cp"`
}

// SellingPriceResult is the outcome of generate-selling-price. RecommendedSP and
// CPPctAtRecommended are pointers so they serialise as JSON null when the raw
// price exceeds the top of the ladder.
type SellingPriceResult struct {
	RawSP              float64  `json:"raw_sp"`
	RecommendedSP      *int     `json:"recommended_sp"`
	CPPctAtRecommended *float64 `json:"cp_pct_at_recommended"`
	PassesNormal       bool     `json:"passes_normal"`
	SurvivesStress     bool     `json:"survives_stress"`
	Warnings           []string `json:"warnings"`
}

// DesignStatusResult is the Green/Yellow/Red verdict with its CP share and the
// human reasons behind any downgrade.
type DesignStatusResult struct {
	Status  DesignStatus `json:"status"`
	CPPct   float64      `json:"cp_pct"`
	Reasons []string     `json:"reasons"`
}
