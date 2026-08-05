package slicing

// Maps a design's answers to Bambu Studio system profiles. Ported from the
// Python worker's profiles.py, then moved from the H2S single-nozzle
// workaround to the shop's real Bambu H2C (dual-nozzle) presets once the
// design form started capturing per-nozzle diameter/flow directly (see
// internal/httpapi/design_machine_link.go).
//
// Headless CLI slicing on a real multi-extruder H2C is still unconfirmed
// (Bambu's CLI has historically refused to map a filament to an extruder on a
// multi-extruder machine - see Optiminastic/Tensor#3) - machineProfile and the
// preset names below are the shop's real BBL H2C system-profile names as
// captured from Bambu Studio's own UI, but headless resolution against them
// has not yet been verified end-to-end (needs a Linux box with the Bambu
// Studio AppImage extracted at BAMBU_ROOT; this environment can't run it).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const machineProfile = "Bambu Lab H2C 0.4 & 0.4 nozzles"

// quality -> process preset (layer height / speed profile). Keys match the
// design form's quality dropdown exactly (internal/httpapi/designs.go's
// validQualities) - the display labels are the literal BBL H2C system preset
// names shown in Bambu Studio.
var qualityProcess = map[string]string{
	"0.08-high":     "0.08mm High Quality @BBL H2C",
	"0.12-high":     "0.12mm High Quality @BBL H2C",
	"0.16-high":     "0.16mm High Quality @BBL H2C",
	"0.16-standard": "0.16mm Standard @BBL H2C",
	"0.20-high":     "0.20mm High Quality @BBL H2C",
	"0.20-standard": "0.20mm Standard @BBL H2C",
	"0.24-standard": "0.24mm Standard @BBL H2C",
}

type filamentProfile struct {
	name    string
	density float64 // g/cm^3, used to derive weight from length
}

// materialFilament keys match the design form's material dropdown exactly
// (internal/httpapi/designs.go's validMaterials). PA-CF's density is an
// approximate industry-typical figure for carbon-fibre-filled nylon, not a
// measured value - refine once real slicing produces one.
var materialFilament = map[string]filamentProfile{
	"PLA Basics": {"Bambu PLA Basic @BBL H2C", 1.24},
	"PA-CF":      {"Bambu PA-CF @BBL H2C", 1.19},
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
		out = append(out, AvailableFilament{Material: material, FilamentPreset: f.name, Density: f.density})
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
	mm, ok := qualityLayerHeightMM[strings.ToLower(quality)]
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

// ResolveProfiles maps (material, quality) to bundled Bambu H2S profiles under
// bambuRoot/resources/profiles/BBL. Errors when a profile is unsupported/missing.
func ResolveProfiles(bambuRoot, material, quality string) (ResolvedProfiles, error) {
	material = strings.TrimSpace(material)
	quality = strings.ToLower(strings.TrimSpace(quality))

	process, ok := qualityProcess[quality]
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
	processPath, err := bblProfile(bambuRoot, "process", process)
	if err != nil {
		return ResolvedProfiles{}, err
	}
	filamentPath, err := bblProfile(bambuRoot, "filament", filament.name)
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
