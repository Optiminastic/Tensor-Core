package slicing

import (
	"encoding/json"
	"testing"
)

func testProfile() PrintProfile {
	return PrintProfile{
		Name: "0.28mm heart", MachineFamily: "h2c", Version: 3,
		Printer: map[string]any{
			"type": "machine", "name": "Bambu Lab H2C 0.4 nozzle", "from": "System", "inherits": "",
			"physical_extruder_map": []any{"1", "0"},
		},
		Process: map[string]any{
			"type": "process", "name": "0.28mm heart", "from": "User",
			"inherits": "0.20mm Standard @BBL H2C", "sparse_infill_density": "10%", "wall_loops": "2",
		},
		Filament: map[string]any{
			"type": "filament", "name": "Bambu PLA Basic @BBL H2C - Copy", "from": "User",
			"inherits": "Bambu PLA Basic @BBL H2C", "filament_colour": []any{"#000000"},
		},
		Overrides: map[string]any{"sparse_infill_density": "15%"},
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuildPresetFilesFollowsTheCLIRules(t *testing.T) {
	files, err := BuildPresetFiles(testProfile(), BedSettings{
		FilamentColours: []string{"#c12e1e", "#FFFFFF"},
		NozzleOverrides: map[string]any{
			"filament_map": []any{"2", "1"}, "filament_map_mode": "Manual",
			"extruder_ams_count": []any{"1#1|4#0", "1#0|4#1"}, "wipe_tower_x": []any{"255"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := decode(t, files.Machine)
	if m["from"] != "User" || m["inherits"] != "Bambu Lab H2C 0.4 nozzle" {
		t.Errorf("a System machine must load as a User preset inheriting itself, got from=%v inherits=%v", m["from"], m["inherits"])
	}
	if m["name"] == "Bambu Lab H2C 0.4 nozzle" {
		t.Error("the machine must not keep the bundled system name; the CLI segfaults on the clash")
	}
	if m["extruder_ams_count"] == nil {
		t.Error("extruder_ams_count belongs on the machine preset")
	}

	p := decode(t, files.Process)
	if p["sparse_infill_density"] != "15%" {
		t.Errorf("the profile's override must win over the imported process, got %v", p["sparse_infill_density"])
	}
	if p["filament_map_mode"] != "Manual" || p["extruder_ams_count"] != nil {
		t.Error("nozzle keys go to the process, except the machine-scoped ones")
	}
	if p["inherits"] != "0.20mm Standard @BBL H2C" {
		t.Errorf("a User process keeps its parent, got %v", p["inherits"])
	}

	if len(files.Filaments) != 2 {
		t.Fatalf("one filament file per slot, got %d", len(files.Filaments))
	}
	f1, f2 := decode(t, files.Filaments[0]), decode(t, files.Filaments[1])
	if f1["name"] == f2["name"] {
		t.Error("two slots loading the same filament name segfaults the CLI")
	}
	if _, ok := f1["inherits"]; ok {
		t.Error("filaments load flat, without inherits")
	}
	if c := f1["filament_colour"].([]any)[0]; c != "#C12E1E" {
		t.Errorf("slot 1 colour, got %v", c)
	}
}

func TestBuildPresetFilesNamesTheVersion(t *testing.T) {
	a := testProfile()
	b := testProfile()
	b.Version = 4
	fa, _ := BuildPresetFiles(a, BedSettings{FilamentColours: []string{"#FFFFFF"}})
	fb, _ := BuildPresetFiles(b, BedSettings{FilamentColours: []string{"#FFFFFF"}})
	if decode(t, fa.Process)["name"] == decode(t, fb.Process)["name"] {
		t.Error("two versions of a profile must render differently named presets")
	}
}

func TestBuildPresetFilesRefusesAnEmptyBed(t *testing.T) {
	if _, err := BuildPresetFiles(testProfile(), BedSettings{}); err == nil {
		t.Error("a bed with no filament slots cannot be sliced")
	}
}

func TestValidateOverrides(t *testing.T) {
	process := testProfile().Process
	cases := []struct {
		name string
		in   map[string]any
		ok   bool
	}{
		{"a known setting", map[string]any{"wall_loops": "3"}, true},
		{"a list value", map[string]any{"wall_loops": []any{"3"}}, true},
		{"a typo", map[string]any{"wall_loop": "3"}, false},
		{"a per-bed key", map[string]any{"filament_map": []any{"1"}}, false},
		{"a number", map[string]any{"wall_loops": 3.0}, false},
	}
	for _, c := range cases {
		err := ValidateOverrides(process, c.in)
		if (err == nil) != c.ok {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}
