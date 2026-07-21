package pricing

import "fmt"

// RemainingMargin is the share of the selling price left after the margin
// components. If adSpendPct is non-nil it overrides the ad-spend component (used
// for the stress test); otherwise the assumption's own ad-spend share is used.
func RemainingMargin(m MarginAssumptions, adSpendPct *float64) float64 {
	ad := m.AdSpendPct
	if adSpendPct != nil {
		ad = *adSpendPct
	}
	return 1 - ad - m.TeamPct - m.OverheadPct - m.TargetProfitPct
}

// SnapUpToLadder returns the first ladder rung greater than or equal to rawSP,
// or nil when the raw price exceeds the top of the ladder. The ladder is assumed
// ascending.
func SnapUpToLadder(rawSP float64, ladder []int) *int {
	for _, price := range ladder {
		if float64(price) >= rawSP {
			p := price
			return &p
		}
	}
	return nil
}

// GenerateSellingPrice reverses the unit economics into a margin-safe selling
// price snapped up to the approved ladder, then stress-tests it.
//
// ladder may be nil, in which case the brand's built-in ladder is used.
// stressAdSpendPct is the ad-spend share for the stress test (the API always
// passes StressAdSpendPct = 0.40). It mirrors generate_selling_price in
// app/pricing/selling_price.py.
func GenerateSellingPrice(
	designCP float64,
	fixed FixedVariableCosts,
	brand Brand,
	margins MarginAssumptions,
	ladder []int,
	stressAdSpendPct float64,
) (SellingPriceResult, error) {
	marginNormal := RemainingMargin(margins, nil)
	if marginNormal <= 0 {
		return SellingPriceResult{}, fmt.Errorf(
			"margin assumptions leave no room for cost; normal margin is not positive",
		)
	}

	otherFixed := fixed.TotalExcludingCP()
	fixedVariable := roundHalfEven(designCP+otherFixed, 2)
	rawSP := roundHalfEven(fixedVariable/marginNormal, 2)

	if ladder == nil {
		ladder = LadderFor(brand)
	}
	recommended := SnapUpToLadder(rawSP, ladder)

	var cpPct *float64
	if recommended != nil {
		v := roundHalfEven(designCP/float64(*recommended), 4)
		cpPct = &v
	}

	marginStress := RemainingMargin(margins, &stressAdSpendPct)
	// Compare the ladder integer against the UNROUNDED break-even under stress,
	// exactly as the Python engine does.
	survivesStress := recommended != nil && marginStress > 0 &&
		float64(*recommended) >= fixedVariable/marginStress

	warnings := rungWarnings(designCP, otherFixed, marginNormal, ladder, recommended)
	if recommended == nil {
		warnings = append(warnings,
			"Raw price exceeds the top of the ladder — review costs or category.")
	}

	return SellingPriceResult{
		RawSP:              rawSP,
		RecommendedSP:      recommended,
		CPPctAtRecommended: cpPct,
		PassesNormal:       recommended != nil,
		SurvivesStress:     survivesStress,
		Warnings:           warnings,
	}, nil
}

// rungWarnings explains, for every rung cheaper than the recommended one, the
// Design CP that rung would need. The strings are reproduced verbatim from the
// Python engine, including the ₹ (U+20B9) and ≤ (U+2264) glyphs, because the
// frontend surfaces them directly. When recommended is nil the loop never breaks
// and every rung gets a warning.
func rungWarnings(designCP, otherFixed, marginNormal float64, ladder []int, recommended *int) []string {
	warnings := []string{}
	for _, price := range ladder {
		if recommended != nil && price >= *recommended {
			break
		}
		maxCP := roundHalfEven(float64(price)*marginNormal-otherFixed, 2)
		if maxCP >= 0 {
			warnings = append(warnings, fmt.Sprintf(
				"₹%d needs Design CP ≤ ₹%s (currently ₹%s).",
				price, formatFixed(maxCP, 0), formatFixed(designCP, 0),
			))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"₹%d is not viable at these fixed costs.", price,
			))
		}
	}
	return warnings
}
