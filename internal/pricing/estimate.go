package pricing

import "unicode"

// PersonalisationSpec describes the text a customer wants added to a product.
type PersonalisationSpec struct {
	Text     string  // the name/engraving
	HeightMM float64 // cap height of the letters
	DepthMM  float64 // emboss/engrave depth
}

// EstimationConfig holds the tunable constants of the pre-slice estimator - a
// fast geometric approximation. The authoritative grams/time still come from the
// slicer; this only powers quotes and the live preview. Admin-tunable later.
type EstimationConfig struct {
	WidthRatio    float64 // character advance as a fraction of height
	Coverage      float64 // fraction of a character's box that is solid material
	SecondsPerMM3 float64 // print time per mm3 of added material
}

// DefaultEstimationConfig returns sane defaults for raised-text personalisation.
func DefaultEstimationConfig() EstimationConfig {
	return EstimationConfig{WidthRatio: 0.62, Coverage: 0.5, SecondsPerMM3: 0.45}
}

// PersonalisationDelta is the estimated extra a name adds over the base product.
type PersonalisationDelta struct {
	VolumeMM3   float64 `json:"volume_mm3"`
	GramsAdded  float64 `json:"grams_added"`
	TimeAddedHr float64 `json:"time_added_hr"`
}

// materialDensity mirrors the slicer's materialFilament table
// (internal/slicing/profiles.go), kept local so the pure engine needs no
// dependency on the slicing package.
var materialDensity = map[string]float64{"PLA": 1.24, "PETG": 1.27, "ABS": 1.04}

// DensityForMaterial returns a material's filament density (g/cm3), defaulting to
// PLA's when the material is unknown.
func DensityForMaterial(material string) float64 {
	if d, ok := materialDensity[material]; ok {
		return d
	}
	return 1.24
}

// EstimateTextVolume approximates the solid volume (mm3) of printing the spec's
// text as raised letters: each visible character occupies a box
// (height x width x depth), of which `coverage` is solid. Volume scales with the
// square of the height (letters grow in two dimensions) times the depth.
func EstimateTextVolume(spec PersonalisationSpec, cfg EstimationConfig) float64 {
	chars := visibleRunes(spec.Text)
	if chars == 0 || spec.HeightMM <= 0 || spec.DepthMM <= 0 {
		return 0
	}
	charBox := spec.HeightMM * (spec.HeightMM * cfg.WidthRatio) * spec.DepthMM
	return float64(chars) * charBox * cfg.Coverage
}

// EstimatePersonalisation folds the estimated added grams and per-unit time into
// the base metrics, returning a SlicerMetrics the unchanged pricing engine
// (ComputeDesignCP / GenerateSellingPrice) consumes, plus the delta.
func EstimatePersonalisation(
	base SlicerMetrics, spec PersonalisationSpec, densityGCm3 float64, cfg EstimationConfig,
) (SlicerMetrics, PersonalisationDelta) {
	vol := EstimateTextVolume(spec, cfg)
	grams := vol / 1000.0 * densityGCm3 // mm3 -> cm3, x g/cm3
	timeHr := vol * cfg.SecondsPerMM3 / 3600.0

	out := base
	out.FilamentG = base.FilamentG + grams
	out.EffectiveMachineTimeHr = base.EffectiveMachineTimeHr + timeHr
	out.PrintTimeHr = base.PrintTimeHr + timeHr
	return out, PersonalisationDelta{VolumeMM3: vol, GramsAdded: grams, TimeAddedHr: timeHr}
}

func visibleRunes(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
