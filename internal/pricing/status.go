package pricing

import "fmt"

// EvaluateStatus produces the Green / Yellow / Red verdict from the Design CP as
// a share of the target selling price, plus the entry-tier machine-time rule.
//
// thresholds is an optional (green_max, yellow_max) pair; when nil the brand's
// built-in thresholds are used. entryMachineHours is optional; entryRung is the
// price the machine-time rule applies at or below (the API passes 999 when the
// brand has no policy). It mirrors evaluate_status in app/pricing/status.py.
func EvaluateStatus(
	designCP, targetSP float64,
	brand Brand,
	metrics *SlicerMetrics,
	thresholds *[2]float64,
	entryMachineHours *float64,
	entryRung int,
) (DesignStatusResult, error) {
	if targetSP <= 0 {
		return DesignStatusResult{}, fmt.Errorf("target selling price must be positive")
	}

	var greenMax, yellowMax float64
	if thresholds != nil {
		greenMax, yellowMax = thresholds[0], thresholds[1]
	} else {
		greenMax, yellowMax = CPThresholds(brand)
	}

	cpPct := roundHalfEven(designCP/targetSP, 4)
	reasons := []string{}

	var status DesignStatus
	switch {
	case cpPct <= greenMax:
		status = StatusGreen
	case cpPct <= yellowMax:
		status = StatusYellow
		reasons = append(reasons, fmt.Sprintf(
			"Design CP is %s of price — above the %s target.",
			formatPercent0(cpPct), formatPercent0(greenMax),
		))
	default:
		status = StatusRed
		reasons = append(reasons, fmt.Sprintf(
			"Design CP is %s of price — over the %s ceiling.",
			formatPercent0(cpPct), formatPercent0(yellowMax),
		))
	}

	// Entry-tier machine-time rule. Gifting with no explicit policy falls back to
	// the built-in (2.0h at ₹999). The rule only ever downgrades GREEN to YELLOW,
	// but it always appends its reason when triggered.
	emh := entryMachineHours
	er := entryRung
	if emh == nil && brand == BrandGifting {
		v := GiftingEntryMachineHours
		emh = &v
		er = GiftingEntryRung
	}

	if emh != nil && targetSP <= float64(er) &&
		metrics != nil && metrics.EffectiveMachineTimeHr > *emh {
		reasons = append(reasons, fmt.Sprintf(
			"Effective machine time %sh exceeds the %sh target for ₹%d.",
			formatFixed(metrics.EffectiveMachineTimeHr, 2), formatFixed(*emh, 0), er,
		))
		if status == StatusGreen {
			status = StatusYellow
		}
	}

	return DesignStatusResult{Status: status, CPPct: cpPct, Reasons: reasons}, nil
}
