package slicing

// Maps a design's answers to Bambu Studio system profiles. Ported from the Python
// worker's profiles.py.
//
// The shop runs a Bambu H2C (dual-nozzle), which cannot be sliced headless from
// presets: Bambu's CLI refuses to map a filament to an extruder on a
// multi-extruder machine and stops at return_code -66. Costing sidesteps that by
// slicing single-colour designs on the H2S profile - the single-nozzle member of
// the same H2 platform (identical speed/flow) - which gives H2-accurate time and
// slices cleanly. See Optiminastic/Tensor#3 and the memory note.
//
// Printing cannot use that substitution: a .gcode.3mf carries the printer it was
// sliced for, so an H2S file is the wrong artefact to send to an H2C. The plate
// path therefore slices from a real H2C project template instead - see
// project3mf.go. Moving costing onto the same template would make quotes truly
// H2C-accurate, but it changes every existing price, so it is a deliberate
// decision rather than a cleanup.

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
	// Family and NozzleMM identify the physical machine, which is what selects a
	// project template for multi-extruder slicing (see project3mf.go). Both are
	// zero on the legacy fixed-profile path, which has no machine behind it.
	Family   string
	NozzleMM float64
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

// ResolveMachineProfiles resolves the Bambu profiles for a machine-driven slice:
// the machine JSON from (family, nozzle), the given filament preset (density is
// the machine's for that filament), and the process preset matched to the layer
// height. The worker has the profile files, so a bad combo fails here loudly.
func ResolveMachineProfiles(
	bambuRoot, family string, nozzleMM float64, filamentPreset string,
	density, layerHeightMM float64,
) (ResolvedProfiles, error) {
	machinePath, err := bblProfile(bambuRoot, "machine", MachineProfileName(family, nozzleMM))
	if err != nil {
		return ResolvedProfiles{}, err
	}
	filamentPath, err := bblProfile(bambuRoot, "filament", filamentPreset)
	if err != nil {
		return ResolvedProfiles{}, err
	}
	process, err := findProcessPreset(bambuRoot, family, nozzleMM, SnapLayerHeight(nozzleMM, layerHeightMM))
	if err != nil {
		return ResolvedProfiles{}, err
	}
	processPath, err := bblProfile(bambuRoot, "process", process)
	if err != nil {
		return ResolvedProfiles{}, err
	}
	return ResolvedProfiles{
		MachinePath: machinePath, ProcessPath: processPath,
		FilamentPath: filamentPath, DensityGCm3: density,
		Family: family, NozzleMM: nozzleMM,
	}, nil
}

// findProcessPreset scans the bundled process profiles for one matching the layer
// height and machine, preferring a "Standard" tier. The 0.4 nozzle is the base
// (suffix "@BBL <family>"); other nozzles carry a nozzle suffix.
func findProcessPreset(bambuRoot, family string, nozzleMM, layerHeightMM float64) (string, error) {
	dir := filepath.Join(bambuRoot, "resources", "profiles", "BBL", "process")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read process profiles: %w", err)
	}
	prefix := fmt.Sprintf("%.2fmm ", layerHeightMM)
	suffix := "@BBL " + family
	if nozzleMM != 0.4 {
		suffix = fmt.Sprintf("@BBL %s %s nozzle", family, trimNozzle(nozzleMM))
	}
	best := ""
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if strings.Contains(name, "Standard") {
			return name, nil
		}
		if best == "" {
			best = name
		}
	}
	if best == "" {
		return "", fmt.Errorf("no process preset for %.2fmm on %s %g nozzle", layerHeightMM, family, nozzleMM)
	}
	return best, nil
}

func bblProfile(root, kind, name string) (string, error) {
	path := filepath.Join(root, "resources", "profiles", "BBL", kind, name+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("missing %s profile: %s", kind, name)
	}
	return path, nil
}
