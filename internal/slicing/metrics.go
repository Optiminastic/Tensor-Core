package slicing

import (
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

const secondsPerHour = 3600.0

// PerUnitMetrics is the per-unit slice result the cost engine consumes, after
// dividing the whole-plate SliceMetrics by units_per_bed. Ported from the Python
// worker's tasks.py:_metrics_payload.
type PerUnitMetrics struct {
	PrintTimeHr            float64
	EffectiveMachineTimeHr float64
	FilamentG              float64
	UnitsPerBed            int
	PurgeG                 float64
	SupportG               float64
	ColourChanges          int
	ElectricityKwh         float64
	LayerHeightMm          float64
	InfillDensityPct       float64
	WallLoops              int
	SupportUsed            bool
	FilamentLengthMm       float64
	GcodeKey               string

	// Orientation is the least-support resting recommendation computed from the
	// model mesh (nil when the format is unsupported or the mesh could not be
	// read). Advisory only; it never changes the costed metrics above.
	Orientation *orientation.Recommendation
}

// ToPerUnit divides the plate totals by units_per_bed and estimates energy from
// print time (the slicer does not report energy), producing the per-unit costing
// input. printerAvgPowerKW is the calibratable average draw.
func ToPerUnit(m SliceMetrics, unitsPerBed int, printerAvgPowerKW float64, gcodeKey string) PerUnitMetrics {
	units := unitsPerBed
	if units < 1 {
		units = 1
	}
	unitsF := float64(units)
	totalTimeHr := m.PrintTimeS / secondsPerHour
	perUnitTimeHr := totalTimeHr / unitsF
	return PerUnitMetrics{
		PrintTimeHr:            roundN(totalTimeHr, 4),
		EffectiveMachineTimeHr: roundN(perUnitTimeHr, 4),
		FilamentG:              roundN(m.FilamentWeightG/unitsF, 3),
		UnitsPerBed:            units,
		PurgeG:                 roundN(m.PurgeWeightG/unitsF, 3),
		SupportG:               roundN(m.SupportWeightG/unitsF, 3),
		ColourChanges:          m.ColourChanges,
		ElectricityKwh:         roundN(perUnitTimeHr*printerAvgPowerKW, 4),
		LayerHeightMm:          m.LayerHeightMM,
		InfillDensityPct:       m.InfillDensityPct,
		WallLoops:              m.WallLoops,
		SupportUsed:            m.SupportUsed,
		FilamentLengthMm:       m.FilamentLengthMM,
		GcodeKey:               gcodeKey,
	}
}

func roundN(v float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(v*factor) / factor
}
