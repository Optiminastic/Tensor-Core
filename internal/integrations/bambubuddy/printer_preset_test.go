package bambubuddy

import (
	"encoding/json"
	"testing"
)

// The H2C's own preset, as /local-presets/5 returns it.
const h2cPresetJSON = `{"name":"Bambu Lab H2C 0.4 nozzle","setting":{
  "printer_model":"Bambu Lab H2C",
  "nozzle_diameter":["0.4","0.4"],
  "physical_extruder_map":["1","0"],
  "extruder_max_nozzle_count":["1","6"]}}`

func presetFrom(t *testing.T, raw string) PrinterPresetSettings {
	t.Helper()
	var p PrinterPresetSettings
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

func TestPhysicalExtruderMapIsReadFromThePreset(t *testing.T) {
	got := presetFrom(t, h2cPresetJSON).PhysicalExtruderMap()
	if len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("map = %v, want [1 0] - the H2C swaps its two numberings", got)
	}
}

func TestPhysicalExtruderMapIsEmptyWhenThePresetDoesNotSayI(t *testing.T) {
	got := presetFrom(t, `{"name":"A2L","setting":{"nozzle_diameter":["0.4"]}}`).PhysicalExtruderMap()
	if got != nil {
		t.Fatalf("map = %v, want nil for a preset that does not place its extruders", got)
	}
}

// The bug this exists to prevent: fixed_nozzle_index is PHYSICAL 1 on every
// H2C here, and filament_map wants LOGICAL 1 - not 2. Adding one to the
// physical index named the AMS nozzle, so Tensor told the slicer to print the
// external spool's colour from the AMS and vice versa.
func TestLogicalExtruderSwapsOnTheH2C(t *testing.T) {
	h2c := []int{1, 0}
	if got, ok := LogicalExtruder(1, h2c); !ok || got != 1 {
		t.Errorf("physical 1 -> %d (ok=%v), want logical 1 (the external spool)", got, ok)
	}
	if got, ok := LogicalExtruder(0, h2c); !ok || got != 2 {
		t.Errorf("physical 0 -> %d (ok=%v), want logical 2 (the AMS)", got, ok)
	}
}

// A single-nozzle machine states no map, and there the two numberings cannot
// disagree - every A2L and P2S bed relies on this staying 1-based.
func TestLogicalExtruderIsOneBasedWithoutAMap(t *testing.T) {
	if got, ok := LogicalExtruder(0, nil); !ok || got != 1 {
		t.Errorf("physical 0 -> %d (ok=%v), want 1", got, ok)
	}
}

// An index the preset does not place must report false, never a guess: the
// wrong answer prints every colour from the wrong nozzle.
func TestLogicalExtruderRefusesAnIndexThePresetDoesNotPlace(t *testing.T) {
	if _, ok := LogicalExtruder(3, []int{1, 0}); ok {
		t.Error("expected a refusal for a physical index the preset does not place")
	}
}

func TestGetPrinterPresetRefusesACloudPreset(t *testing.T) {
	c := &Client{}
	if _, err := c.GetPrinterPreset(t.Context(), PresetVal{Source: "cloud", ID: "x"}); err == nil {
		t.Error("expected a refusal: a cloud preset exposes no raw settings to read")
	}
}
