package pricing

import "fmt"

// ComputeEffectiveMachineTime returns the per-unit print time after batching
// units_per_bed units on one bed, rounded to 4 dp. It mirrors
// compute_effective_machine_time in app/pricing/design_cp.py.
func ComputeEffectiveMachineTime(batchPrintTimeHr float64, unitsPerBed int) (float64, error) {
	if unitsPerBed < 1 {
		return 0, fmt.Errorf("units_per_bed must be at least 1")
	}
	if batchPrintTimeHr <= 0 {
		return 0, fmt.Errorf("batch_print_time_hr must be positive")
	}
	return roundHalfEven(batchPrintTimeHr/float64(unitsPerBed), 4), nil
}

// ComputeDesignCP builds the true per-unit manufacturing cost from slicer
// metrics and cost assumptions, plus a failure provision.
//
// The subtotal, failure provision and design CP are derived from the UNROUNDED
// components; only the emitted breakdown fields are rounded to 2 dp. This
// ordering is deliberate and matches the Python engine -- rounding the
// components first would let paisa-level error accumulate into the total.
func ComputeDesignCP(m SlicerMetrics, a CostAssumptions) DesignCPBreakdown {
	filament := m.FilamentG / 1000 * a.FilamentCostPerKg
	purge := m.PurgeG / 1000 * a.FilamentCostPerKg
	electricity := m.ElectricityKwh * a.ElectricityCostPerUnit
	machine := m.EffectiveMachineTimeHr * a.MachineHourCost
	finishing := a.FinishingLabour
	consumables := a.Consumables

	subtotal := filament + purge + electricity + machine + finishing + consumables
	failureProvision := subtotal * a.FailurePct
	designCP := subtotal + failureProvision

	return DesignCPBreakdown{
		Filament:         roundHalfEven(filament, 2),
		Purge:            roundHalfEven(purge, 2),
		Electricity:      roundHalfEven(electricity, 2),
		Machine:          roundHalfEven(machine, 2),
		Finishing:        roundHalfEven(finishing, 2),
		Consumables:      roundHalfEven(consumables, 2),
		FailureProvision: roundHalfEven(failureProvision, 2),
		Subtotal:         roundHalfEven(subtotal, 2),
		DesignCP:         roundHalfEven(designCP, 2),
	}
}
