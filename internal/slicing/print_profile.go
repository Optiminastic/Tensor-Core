package slicing

// Turns a stored slicing profile into the preset files the Bambu Studio CLI
// loads to slice a bed for printing.
//
// A profile holds BambuBuddy's own presets, unedited, so a Tensor slice uses
// the settings a BambuBuddy slice of the same pipeline would. Loading them into
// the CLI as they are does not work, though, and each rule below is a crash or
// a refusal seen in the 2026-11-20 spike on a real H2C two-colour bed:
//
//   - Every preset needs a name of its own. Loading one filament file twice for
//     two slots segfaults, and so does a machine preset named exactly like the
//     bundled system one ("Bambu Lab H2C 0.4 nozzle").
//   - A machine preset marked "from": "System" is refused (return -5). It is
//     loaded as a User preset that inherits the system one by name. Dropping
//     the inherits instead fails as "printer is not compatible with the process
//     preset".
//   - Filaments are loaded flat, without inherits. BambuBuddy's filament
//     presets carry every key, and one filament file per colour slot is what
//     gives the plate its real colours and weights.
//
// With those rules the CLI reproduced BambuBuddy's slice of batch
// 116688-116706: the same grams per filament, the same nozzle map and the
// same tool-change sequence.

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// PrintProfile is one stored slicing profile, decoded.
type PrintProfile struct {
	Name          string
	MachineFamily string
	Version       int
	Printer       map[string]any
	Process       map[string]any
	Filament      map[string]any
	// Overrides are Tensor's own process changes on top of Process.
	Overrides map[string]any
}

// BedSettings is what belongs to one bed on one machine rather than to the
// product: the colour in each filament slot and the nozzle-map keys
// (filament_map, extruder_ams_count, the prime tower) computed at dispatch.
type BedSettings struct {
	// FilamentColours holds one "#RRGGBB" per plate filament slot, in slot order.
	FilamentColours []string
	NozzleOverrides map[string]any
}

// PresetFiles are the JSON documents passed to --load-settings (Machine and
// Process) and --load-filaments (Filaments, one per slot).
type PresetFiles struct {
	Machine   []byte
	Process   []byte
	Filaments [][]byte
}

// machineScopedKeys are nozzle-map keys that live on the machine preset, not
// on the process. extruder_ams_count tells the slicer which AMS feeds which
// nozzle, and it is read from the printer's settings.
var machineScopedKeys = map[string]bool{"extruder_ams_count": true}

// ReservedOverrideKeys are computed for each bed at dispatch, so a profile
// may not set them: a stored value would be wrong on the next machine or the
// next set of trays.
var ReservedOverrideKeys = map[string]bool{
	"filament_map":       true,
	"filament_map_mode":  true,
	"extruder_ams_count": true,
	"filament_colour":    true,
	"enable_prime_tower": true,
	"wipe_tower_x":       true,
	"wipe_tower_y":       true,
	"prime_tower_width":  true,
}

// BuildPresetFiles renders a profile and one bed's settings into CLI preset files.
func BuildPresetFiles(p PrintProfile, bed BedSettings) (PresetFiles, error) {
	if len(p.Printer) == 0 || len(p.Process) == 0 || len(p.Filament) == 0 {
		return PresetFiles{}, fmt.Errorf("profile %q is missing a printer, process or filament preset", p.Name)
	}
	if len(bed.FilamentColours) == 0 {
		return PresetFiles{}, fmt.Errorf("a bed needs at least one filament colour")
	}
	tag := profileTag(p)

	machine := userPreset(p.Printer, "machine", tag+" machine")
	process := userPreset(p.Process, "process", tag+" process")
	maps.Copy(process, p.Overrides)
	for k, v := range bed.NozzleOverrides {
		if machineScopedKeys[k] {
			machine[k] = v
		} else {
			process[k] = v
		}
	}

	out := PresetFiles{}
	var err error
	if out.Machine, err = json.Marshal(machine); err != nil {
		return PresetFiles{}, err
	}
	if out.Process, err = json.Marshal(process); err != nil {
		return PresetFiles{}, err
	}
	for i, colour := range bed.FilamentColours {
		f := maps.Clone(p.Filament)
		delete(f, "inherits")
		f["from"] = "User"
		f["type"] = "filament"
		f["name"] = fmt.Sprintf("%s filament %d", tag, i+1)
		f["filament_colour"] = []any{strings.ToUpper(colour)}
		b, err := json.Marshal(f)
		if err != nil {
			return PresetFiles{}, err
		}
		out.Filaments = append(out.Filaments, b)
	}
	return out, nil
}

// userPreset copies a preset as a uniquely named User preset. A System preset
// is made to inherit itself by name, which is how the CLI accepts it.
//
// The type is always set: the CLI refuses a preset without one ("unknown
// config type"), and BambuBuddy's copy of a system printer preset (the P2S
// one, 2026-11-20) carries none.
func userPreset(src map[string]any, kind, name string) map[string]any {
	dst := maps.Clone(src)
	if from, _ := src["from"].(string); strings.EqualFold(from, "system") {
		if own, _ := src["name"].(string); own != "" {
			dst["inherits"] = own
		}
	}
	dst["from"] = "User"
	dst["type"] = kind
	dst["name"] = name
	return dst
}

// profileTag names every preset of one profile version, so two versions of a
// profile can never collide and a slice's settings name the version they came from.
func profileTag(p PrintProfile) string {
	return fmt.Sprintf("Tensor %s %s v%d", strings.ToUpper(p.MachineFamily), p.Name, p.Version)
}

// ValidateOverrides checks a profile's overrides against its process preset:
// each key must already be a process setting (catching typos that the slicer
// would silently ignore) and must not be one computed per bed. Values are
// strings or lists of strings, which is all Bambu's preset format holds.
func ValidateOverrides(process map[string]any, overrides map[string]any) error {
	for k, v := range overrides {
		if ReservedOverrideKeys[k] {
			return fmt.Errorf("%s is set for each bed at dispatch and cannot be part of a profile", k)
		}
		if _, ok := process[k]; !ok {
			return fmt.Errorf("%s is not a setting of this profile's process preset", k)
		}
		if !presetValue(v) {
			return fmt.Errorf("%s must be text or a list of text, as in a Bambu preset", k)
		}
	}
	return nil
}

func presetValue(v any) bool {
	switch t := v.(type) {
	case string:
		return true
	case []any:
		for _, e := range t {
			if _, ok := e.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}
