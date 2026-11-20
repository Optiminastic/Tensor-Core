package slicing

import (
	"os"
	"path/filepath"
	"testing"
)

func writePreset(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bundledProcesses(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "resources", "profiles", "BBL", "process")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePreset(t, dir, "fdm_process_common.json",
		`{"name":"fdm_process_common","from":"system","support_ironing_spacing":"0.1","wall_loops":"1"}`)
	writePreset(t, dir, "fdm_process_dual_common.json",
		`{"name":"fdm_process_dual_common","inherits":"fdm_process_common","support_ironing_spacing":"0.15","monotonic_travel_into_wall":"45%"}`)
	writePreset(t, dir, "std.json",
		`{"name":"0.20mm Standard @BBL H2C","inherits":"fdm_process_dual_common","instantiation":"true","layer_height":"0.2"}`)
	writePreset(t, dir, "notes.txt", "not a preset")
	writePreset(t, dir, "broken.json", "{")
	return root
}

func TestFillFromChainTakesTheNearestParentAndKeepsThePreset(t *testing.T) {
	sys, err := LoadSystemPresets(bundledProcesses(t), "process")
	if err != nil {
		t.Fatal(err)
	}
	preset := map[string]any{
		"name": "0.28mm heart", "from": "User", "inherits": "0.20mm Standard @BBL H2C",
		"layer_height": "0.28",
	}
	added, err := sys.FillFromChain(preset)
	if err != nil {
		t.Fatal(err)
	}
	if preset["layer_height"] != "0.28" {
		t.Error("a key the preset sets must never be replaced by a parent's")
	}
	if preset["support_ironing_spacing"] != "0.15" {
		t.Errorf("the nearest parent wins, got %v", preset["support_ironing_spacing"])
	}
	if preset["wall_loops"] != "1" || preset["monotonic_travel_into_wall"] != "45%" {
		t.Error("every level of the chain contributes its keys")
	}
	if preset["name"] != "0.28mm heart" || preset["inherits"] != "0.20mm Standard @BBL H2C" {
		t.Error("meta keys stay the preset's own")
	}
	if _, ok := preset["instantiation"]; !ok {
		// instantiation is meta and the preset never set it: it must stay unset.
	} else {
		t.Error("meta keys are never copied from a parent")
	}
	if added != 3 {
		t.Errorf("added %d keys, want 3", added)
	}
}

func TestFillFromChainRefusesAParentThisInstallLacks(t *testing.T) {
	sys, err := LoadSystemPresets(bundledProcesses(t), "process")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sys.FillFromChain(map[string]any{"inherits": "0.20mm Standard @BBL H9X"})
	if err == nil {
		t.Error("a parent missing from the install means a different Bambu Studio version; refuse")
	}
}

func TestFillFromChainLeavesAFlatPresetAlone(t *testing.T) {
	sys := SystemPresets{}
	added, err := sys.FillFromChain(map[string]any{"name": "flat"})
	if err != nil || added != 0 {
		t.Errorf("got %d, %v", added, err)
	}
}
