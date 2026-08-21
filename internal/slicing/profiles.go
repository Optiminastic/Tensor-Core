package slicing

// Maps a design's answers to Bambu Studio system profiles. Ported from the
// Python worker's profiles.py.
//
// # Why H2S and not the shop's H2C
//
// This briefly pointed at the shop's real Bambu H2C (dual-nozzle) presets,
// under the name "Bambu Lab H2C 0.4 & 0.4 nozzles". Slicing every design in
// the Dockerised worker (sliceworker.Dockerfile) proved that wrong twice over:
//
//  1. That name is Bambu Studio's *UI display* label. No such file is bundled
//     - the machine profiles are named "Bambu Lab H2C 0.4 nozzle.json" - so
//     ResolveProfiles failed with "missing machine profile" before Bambu was
//     ever invoked. Every real slice failed; only FAKE_SLICE hid it.
//
//  2. Fixing the name only got as far as the next wall. The H2C is genuinely
//     multi-extruder (nozzle_diameter ["0.4","0.4"]), and its CLI refuses to
//     assign a filament to an extruder headless:
//
//     plate 1 : some filaments can not be mapped under auto mode for
//     multi extruder printer / return -66
//
//     That is Optiminastic/Tensor#3, now confirmed rather than suspected.
//     --estimate-mode (rejected: wants a machine switch, not a bare model),
//     --load-filament-ids, --filament-map, --filament-map-mode Manual and
//     passing one filament per extruder were all tried; every one fails the
//     same way.
//
// So slicing runs against the H2S - the H2C's own single-extruder sibling:
// same printer generation, same 0.4 nozzle, same bed, and a complete set of
// presets for all seven qualities and both materials. It slices cleanly
// headless. A single-extruder estimate for a part that will print on one
// nozzle anyway is a real measurement, not a proxy from a different class of
// machine - but a genuinely two-material print would be estimated as if it
// were run in one material, so a multi-material design's time and filament
// split are NOT trustworthy until the H2C CLI can map extruders.
//
// Switching back is deliberately a two-line change: set machineProfile and
// presetSuffix, and re-run one real slice to confirm the CLI cooperates.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	machineProfile = "Bambu Lab H2S 0.4 nozzle"
	// presetSuffix is the "@BBL <model>" tail every process and filament
	// preset name carries. It must match machineProfile's printer model:
	// Bambu resolves a process/filament preset against the loaded machine,
	// and a mismatched pair is not a supported combination.
	presetSuffix = "@BBL H2S"
)

// quality -> process preset (layer height / speed profile), without the
// machine suffix. Keys match the design form's quality dropdown exactly
// (internal/httpapi/designs.go's validQualities); the values are the literal
// system preset names shown in Bambu Studio.
var qualityProcess = map[string]string{
	"0.08-high":     "0.08mm High Quality",
	"0.12-high":     "0.12mm High Quality",
	"0.16-high":     "0.16mm High Quality",
	"0.16-standard": "0.16mm Standard",
	"0.20-high":     "0.20mm High Quality",
	"0.20-standard": "0.20mm Standard",
	"0.24-standard": "0.24mm Standard",
}

type filamentProfile struct {
	name    string
	density float64 // g/cm^3, used to derive weight from length
}

// materialFilament keys match the design form's material dropdown exactly
// (internal/httpapi/designs.go's validMaterials); names carry no machine
// suffix (see presetName). PA-CF's density is an approximate industry-typical
// figure for carbon-fibre-filled nylon, not a measured value - refine once
// real slicing produces one.
var materialFilament = map[string]filamentProfile{
	"PLA Basics": {"Bambu PLA Basic", 1.24},
	"PA-CF":      {"Bambu PA-CF", 1.19},
}

// presetName qualifies a bare preset name with the machine suffix, e.g.
// "0.20mm Standard" -> "0.20mm Standard @BBL H2S".
func presetName(base string) string { return base + " " + presetSuffix }

// qualityAliases map the shorthand quality values older designs were created
// with onto today's explicit layer-height keys.
//
// The design form once offered "draft"/"standard"/"fine" without a layer
// height; it now asks for the height directly. Designs created under the old
// form still carry the old value, and without this they fail to slice at all
// ("unknown quality: standard") - which in practice meant their batches never
// got a measured print time and could never be committed to a machine. Mapping
// them to the nearest current preset is better than refusing to slice work that
// is otherwise perfectly printable.
var qualityAliases = map[string]string{
	"draft":    "0.24-standard",
	"standard": "0.20-standard",
	"fine":     "0.16-high",
	"high":     "0.12-high",
}

// canonicalQuality resolves a design's quality answer to a key qualityProcess
// knows, translating a legacy shorthand if that is what it is.
func canonicalQuality(quality string) string {
	if canonical, ok := qualityAliases[quality]; ok {
		return canonical
	}
	return quality
}

// AvailableFilament is one entry in the machine-admin profile-options catalog.
type AvailableFilament struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
}

// AvailableFilaments lists every material this shop can slice, for the machine
// admin form's (family, nozzle) profile-options catalog. Currently the same
// set regardless of family/nozzle, since only the bundled H2C profile set is
// wired up today.
func AvailableFilaments() []AvailableFilament {
	out := make([]AvailableFilament, 0, len(materialFilament))
	for material, f := range materialFilament {
		out = append(out, AvailableFilament{Material: material, FilamentPreset: presetName(f.name), Density: f.density})
	}
	return out
}

// qualityLayerHeightMM mirrors qualityProcess's keys with their numeric layer
// height (mm), read off each process preset's own name.
var qualityLayerHeightMM = map[string]float64{
	"0.08-high":     0.08,
	"0.12-high":     0.12,
	"0.16-high":     0.16,
	"0.16-standard": 0.16,
	"0.20-high":     0.20,
	"0.20-standard": 0.20,
	"0.24-standard": 0.24,
}

// AvailableLayerHeights lists every layer height (mm) a quality preset maps to.
func AvailableLayerHeights() []float64 {
	out := make([]float64, 0, len(qualityLayerHeightMM))
	for _, mm := range qualityLayerHeightMM {
		out = append(out, mm)
	}
	return out
}

// LayerHeightMM converts a design's quality answer (draft/standard/fine) to
// its numeric layer height, for snapshotting onto a matched job. Nil for an
// unrecognised quality.
func LayerHeightMM(quality string) *float64 {
	mm, ok := qualityLayerHeightMM[canonicalQuality(strings.ToLower(quality))]
	if !ok {
		return nil
	}
	return &mm
}

// ResolvedProfiles are the Bambu profile file paths + filament density for a slice.
type ResolvedProfiles struct {
	MachinePath  string
	ProcessPath  string
	FilamentPath string
	DensityGCm3  float64
}

// ResolveProfiles maps (material, quality) to the bundled Bambu profiles under
// bambuRoot/resources/profiles/BBL. Errors when a profile is unsupported/missing.
func ResolveProfiles(bambuRoot, material, quality string) (ResolvedProfiles, error) {
	material = strings.TrimSpace(material)
	quality = strings.ToLower(strings.TrimSpace(quality))

	process, ok := qualityProcess[canonicalQuality(quality)]
	if !ok {
		return ResolvedProfiles{}, fmt.Errorf("unknown quality: %s", quality)
	}
	filament, ok := materialFilament[material]
	if !ok {
		return ResolvedProfiles{}, fmt.Errorf("unsupported material: %s", material)
	}

	machinePath, err := bblProfile(bambuRoot, "machine", machineProfile)
	if err != nil {
		return ResolvedProfiles{}, err
	}
	processPath, err := bblProfile(bambuRoot, "process", presetName(process))
	if err != nil {
		return ResolvedProfiles{}, err
	}
	filamentPath, err := bblProfile(bambuRoot, "filament", presetName(filament.name))
	if err != nil {
		return ResolvedProfiles{}, err
	}
	return ResolvedProfiles{
		MachinePath:  machinePath,
		ProcessPath:  processPath,
		FilamentPath: filamentPath,
		DensityGCm3:  filament.density,
	}, nil
}

func bblProfile(root, kind, name string) (string, error) {
	path := filepath.Join(root, "resources", "profiles", "BBL", kind, name+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("missing %s profile: %s", kind, name)
	}
	return path, nil
}
