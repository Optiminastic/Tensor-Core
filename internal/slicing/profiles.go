package slicing

// Maps a design's answers to Bambu Studio system profiles. Ported from the Python
// worker's profiles.py.
//
// The shop runs a Bambu H2C (dual-nozzle), which cannot slice headless (Bambu's
// CLI refuses to map a filament to an extruder on a multi-extruder machine). We
// slice single-colour designs on the H2S profile - the single-nozzle member of
// the same H2 platform (identical speed/flow) - which gives H2-accurate time and
// slices cleanly. See Optiminastic/Tensor#3 and the memory note. To switch to real
// H2C later, change machineProfile and the @BBL suffixes below.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const machineProfile = "Bambu Lab H2S 0.4 nozzle"

// quality -> process preset (layer height / speed profile).
var qualityProcess = map[string]string{
	"draft":    "0.24mm Standard @BBL H2S",
	"standard": "0.20mm Standard @BBL H2S",
	"fine":     "0.12mm High Quality @BBL H2S",
}

type filamentProfile struct {
	name    string
	density float64 // g/cm^3, used to derive weight from length
}

var materialFilament = map[string]filamentProfile{
	"PLA":  {"Bambu PLA Basic @BBL H2S", 1.24},
	"PETG": {"Bambu PETG Basic @BBL H2S", 1.27},
	"ABS":  {"Bambu ABS @BBL H2S", 1.04},
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
	material = strings.ToUpper(material)
	quality = strings.ToLower(quality)

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
