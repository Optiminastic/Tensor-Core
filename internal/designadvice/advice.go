// Package designadvice turns a design's slicer metrics into concrete, human fix
// suggestions - the "tell the designer specifically why" step of the pre-check.
// It is pure: metrics in, ordered suggestion strings out. It never touches cost;
// the cost engine owns the number, this only advises how to move it.
package designadvice

import "fmt"

// Thresholds above which a metric is worth a suggestion. Deliberately plain
// constants (calibratable) rather than magic numbers scattered in the rules.
const (
	highInfillPct       = 25.0
	heavyFilamentGrams  = 80.0
	singleUnitBatchHint = 1
	// Minimum estimated overhang reduction (fraction) before the generic "reorient
	// it" advice is replaced with the specific computed rotation.
	orientationSuggestPct = 0.15
)

// Input is the design's per-unit slice facts plus the verdict and the brand's
// entry machine-time target (nil when the brand has no such rule).
type Input struct {
	Verdict                string // "green" | "yellow" | "red"
	FilamentG              float64
	EffectiveMachineTimeHr float64
	InfillPct              float64
	SupportUsed            bool
	UnitsPerBed            int
	EntryMachineHours      *float64

	// OrientationHint is the computed rotation instruction (empty when none was
	// found); OrientationReductionPct is its estimated overhang reduction (0..1).
	OrientationHint         string
	OrientationReductionPct float64
}

// Suggest returns ordered fix suggestions, most impactful first. A green design
// with nothing notable returns an empty slice.
func Suggest(in Input) []string {
	out := make([]string, 0, 4)

	if in.SupportUsed {
		out = append(out, supportSuggestion(in))
	}
	if in.InfillPct > highInfillPct {
		out = append(out, fmt.Sprintf("Infill is %.0f%%. Most decorative parts hold up at 15%%; lowering it reduces filament and print time.", in.InfillPct))
	}
	if in.EntryMachineHours != nil && in.EffectiveMachineTimeHr > *in.EntryMachineHours {
		out = append(out, fmt.Sprintf("Effective machine time is %.2fh, above the %.0fh target. Batching more units per bed or using a coarser layer height lowers the per-unit time.", in.EffectiveMachineTimeHr, *in.EntryMachineHours))
	} else if in.UnitsPerBed <= singleUnitBatchHint && in.Verdict != "green" {
		out = append(out, "Only one unit per bed. Printing several at once spreads the fixed per-plate time across units and lowers the per-unit cost.")
	}
	if in.FilamentG > heavyFilamentGrams {
		out = append(out, fmt.Sprintf("Filament use is %.0fg. Hollowing the model or reducing wall count lowers material cost.", in.FilamentG))
	}

	if in.Verdict == "red" && len(out) == 0 {
		out = append(out, "Design CP is too high for the target price. The largest levers are print time and filament - reduce infill, reorient to drop supports, or batch more units per bed.")
	}
	return out
}

// supportSuggestion prefers the specific computed rotation when the orientation
// analysis found a worthwhile improvement, falling back to the generic advice.
func supportSuggestion(in Input) string {
	if in.OrientationHint != "" && in.OrientationReductionPct >= orientationSuggestPct {
		return fmt.Sprintf(
			"%s That could cut the support-generating overhang by roughly %.0f%%.",
			in.OrientationHint, in.OrientationReductionPct*100,
		)
	}
	return "This design needs support material. Reorienting it or adding chamfers instead of overhangs can remove supports, cutting both filament and print time."
}
