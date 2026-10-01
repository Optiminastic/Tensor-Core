package bambubuddy

// Reading a printer preset's own settings, for the one thing Tensor cannot
// guess: how the machine numbers its extruders.
//
// A two-nozzle Bambu has TWO numberings and they are not the same. The printer
// reports PHYSICAL extruder indices - extruder_slots keys, active_extruder,
// the feed carrying ams_id 254. The sliced file uses LOGICAL ones -
// filament_map's "1" and "2". physical_extruder_map translates between them,
// and on the H2C it is [1,0]: the two are SWAPPED, not offset.
//
// Tensor used to convert by adding one, which is right only when the map is the
// identity. On an H2C it lands on the opposite nozzle, so every plate was told
// to print the external spool's colour from the AMS nozzle and the AMS colours
// from the external spool - the exact inverse of the machine. Every file Bambu
// Studio produced for these printers carries physical_extruder_map = 1,0.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PrinterPresetSettings is a printer preset's raw settings.
type PrinterPresetSettings struct {
	Name    string            `json:"name"`
	Setting map[string]any    `json:"setting"`
	Extra   map[string]string `json:"-"`
}

// PhysicalExtruderMap is the preset's logical-to-physical extruder map.
//
// Index is the LOGICAL extruder (filament_map value minus one); the value is
// the PHYSICAL index the printer reports. Empty when the preset does not say,
// which is every single-nozzle machine - there the two numberings cannot
// disagree.
func (p PrinterPresetSettings) PhysicalExtruderMap() []int {
	raw, ok := p.Setting["physical_extruder_map"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// GetPrinterPreset reads one printer preset's settings.
//
// Local presets only. The shop's H2C, A2L and P2S profiles are all local
// imports, and a cloud preset has no endpoint that returns its raw settings -
// so an unknown source reports an error rather than a map that silently lacks
// the one key this exists to read.
func (c *Client) GetPrinterPreset(ctx context.Context, p PresetVal) (PrinterPresetSettings, error) {
	if !strings.EqualFold(p.Source, "local") {
		return PrinterPresetSettings{}, fmt.Errorf(
			"bambubuddy printer preset: %q presets carry no readable settings", p.Source)
	}
	var out PrinterPresetSettings
	path := fmt.Sprintf("/api/v1/local-presets/%s", p.ID)
	if err := c.get(ctx, path, &out); err != nil {
		return PrinterPresetSettings{}, err
	}
	return out, nil
}

// LogicalExtruder turns a physical extruder index into the filament_map value
// that addresses it.
//
// Reports false when the physical index is not in the map at all, because a
// wrong answer here prints every colour from the wrong nozzle - far worse than
// declining to pin the map and letting the slicer arrange it.
func LogicalExtruder(physical int, physicalMap []int) (int, bool) {
	if len(physicalMap) == 0 {
		// No map means one nozzle, where logical and physical agree and the
		// value is simply 1-based.
		return physical + 1, true
	}
	for logical, p := range physicalMap {
		if p == physical {
			return logical + 1, true
		}
	}
	return 0, false
}

// decode keeps the Setting map usable when BambuBuddy nests it.
func (p *PrinterPresetSettings) UnmarshalJSON(data []byte) error {
	type alias struct {
		Name    string         `json:"name"`
		Setting map[string]any `json:"setting"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	p.Name, p.Setting = a.Name, a.Setting
	if p.Setting == nil {
		p.Setting = map[string]any{}
	}
	return nil
}
