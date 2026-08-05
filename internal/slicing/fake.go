package slicing

// Dev-only fabricated slice results, used when Bambu Studio can't actually run
// (config.Settings.FakeSlice / FAKE_SLICE=true - see cmd/sliceworker). Produces
// plausible per-unit numbers, scaled by the design's own answers, so a design
// can flow through pricing and approval for testing the downstream pipeline
// (SKU match, batching, scheduling) without a real slice. The numbers are
// deliberately modest (low filament, well under the 2h entry-tier machine-time
// threshold) so a fake design prices GREEN rather than looking broken.
//
// NEVER enable FAKE_SLICE against a production deployment - every number here
// is invented, not measured.

import "fmt"

const (
	fakeBaseFilamentG   = 20.0
	fakeInfillFilamentG = 15.0
	fakeBasePrintHr     = 0.35
	fakeInfillPrintHr   = 0.5
	fakePurgeG          = 1.5
	fakeWallLoops       = 2
	// Rough PLA weight-to-length ratio at 1.75mm (~0.0033 g/mm); not used in
	// costing (pricing.SlicerMetrics has no filament-length field), only stored
	// for the design's metrics display.
	fakeMMPerGram = 303.0
)

// FakeSlice fabricates PerUnitMetrics for one job. printerAvgPowerKW mirrors
// the real slice() path's electricity estimate so a fake design's numbers stay
// internally consistent with a real one's.
func FakeSlice(args SliceArgs, printerAvgPowerKW float64) PerUnitMetrics {
	units := args.UnitsPerBed
	if units < 1 {
		units = 1
	}
	infill := args.InfillPct
	if infill <= 0 {
		infill = defaultInfillPct
	}
	infillFrac := infill / 100.0

	printTimeHr := fakeBasePrintHr + infillFrac*fakeInfillPrintHr
	filamentG := fakeBaseFilamentG + infillFrac*fakeInfillFilamentG
	layerHeight := 0.2
	if mm := LayerHeightMM(args.Quality); mm != nil {
		layerHeight = *mm
	}

	return PerUnitMetrics{
		PrintTimeHr:            printTimeHr,
		EffectiveMachineTimeHr: printTimeHr,
		FilamentG:              filamentG,
		UnitsPerBed:            units,
		PurgeG:                 fakePurgeG,
		SupportG:               0,
		ColourChanges:          0,
		ElectricityKwh:         printTimeHr * printerAvgPowerKW,
		LayerHeightMm:          layerHeight,
		InfillDensityPct:       infill,
		WallLoops:              fakeWallLoops,
		SupportUsed:            false,
		FilamentLengthMm:       filamentG * fakeMMPerGram,
		GcodeKey:               fmt.Sprintf("gcode/%s.fake.3mf", args.JobID),
	}
}
