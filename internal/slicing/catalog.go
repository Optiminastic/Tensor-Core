package slicing

import (
	"fmt"
	"math"
)

// Curated catalog of what the bundled Bambu build can slice, keyed to the pinned
// AppImage version. The API process runs on the host and cannot scan the profiles
// (they live inside the slice-worker image), so the Machine Settings form is driven
// by this table. The worker (which HAS the files) still validates the resolved
// paths at slice time, so a stale entry fails loudly rather than silently.

// SliceableFamilies are the Bambu platforms that slice headless (single-nozzle H2
// members; the real H2C dual-nozzle cannot slice headless).
var SliceableFamilies = []string{"H2S"}

// nozzleLayerHeights lists the layer heights (mm) that have a process preset for
// each nozzle, from the bundled H2S process profiles.
var nozzleLayerHeights = map[float64][]float64{
	0.2: {0.08, 0.10, 0.12},
	0.4: {0.08, 0.12, 0.16, 0.20, 0.24},
	0.6: {0.18, 0.24, 0.30},
	0.8: {0.24, 0.32, 0.40},
}

// catalogFilament is one filament the platform ships. minNozzle bars abrasive /
// carbon-fibre filaments from the smallest nozzle.
type catalogFilament struct {
	Material  string // the label AND the preset base ("Bambu <Material> @BBL <family>")
	Density   float64
	MinNozzle float64
}

// h2sFilaments is the curated H2S filament set (name, density g/cm3, min nozzle).
var h2sFilaments = []catalogFilament{
	{"PLA Basic", 1.24, 0.2},
	{"PLA Matte", 1.24, 0.2},
	{"PLA-CF", 1.22, 0.4},
	{"PETG Basic", 1.27, 0.2},
	{"PETG HF", 1.27, 0.4},
	{"ABS", 1.04, 0.2},
	{"ASA", 1.07, 0.4},
	{"PA-CF", 1.19, 0.4},
	{"PA6-CF", 1.19, 0.4},
	{"PAHT-CF", 1.19, 0.4},
	{"TPU 95A", 1.20, 0.4},
}

// FilamentOption is a filament offered for a machine's (family, nozzle).
type FilamentOption struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
}

// ProfileOptionsResult is what the settings form needs to offer only-valid config.
type ProfileOptionsResult struct {
	Filaments    []FilamentOption `json:"filaments"`
	LayerHeights []float64        `json:"layer_heights"`
}

// MachineProfileName is the bundled machine JSON name for a (family, nozzle):
// "Bambu Lab <family> <nozzle> nozzle".
func MachineProfileName(family string, nozzleMM float64) string {
	return fmt.Sprintf("Bambu Lab %s %s nozzle", family, trimNozzle(nozzleMM))
}

// FilamentPreset is the bundled filament preset name for a material on a machine.
// The 0.4 nozzle is the base (no suffix); other nozzles carry a nozzle suffix.
func FilamentPreset(family string, nozzleMM float64, material string) string {
	base := fmt.Sprintf("Bambu %s @BBL %s", material, family)
	if nozzleMM == 0.4 {
		return base
	}
	return fmt.Sprintf("%s %s nozzle", base, trimNozzle(nozzleMM))
}

// ProfileOptions returns the filaments and layer heights valid for a (family,
// nozzle). Unknown families/nozzles yield empty lists.
func ProfileOptions(family string, nozzleMM float64) ProfileOptionsResult {
	res := ProfileOptionsResult{Filaments: []FilamentOption{}, LayerHeights: []float64{}}
	if family != "H2S" {
		return res
	}
	res.LayerHeights = append(res.LayerHeights, nozzleLayerHeights[nozzleMM]...)
	for _, f := range h2sFilaments {
		if nozzleMM >= f.MinNozzle {
			res.Filaments = append(res.Filaments, FilamentOption{
				Material:       f.Material,
				FilamentPreset: FilamentPreset(family, nozzleMM, f.Material),
				Density:        f.Density,
			})
		}
	}
	return res
}

// trimNozzle formats a nozzle diameter the way the bundled files name it: "0.4",
// "0.6" (no trailing zeros beyond one decimal).
func trimNozzle(nozzleMM float64) string {
	return fmt.Sprintf("%g", nozzleMM)
}

// SnapLayerHeight returns the available layer height nearest to `requested` for a
// nozzle, so a machine slice always maps to a real process preset. Returns the
// request unchanged when the nozzle is unknown.
func SnapLayerHeight(nozzleMM, requested float64) float64 {
	options := nozzleLayerHeights[nozzleMM]
	if len(options) == 0 {
		return requested
	}
	best := options[0]
	for _, o := range options {
		if math.Abs(o-requested) < math.Abs(best-requested) {
			best = o
		}
	}
	return best
}
