package slicing

import (
	"os"
	"path/filepath"
	"testing"
)

func bundledFilaments(t *testing.T) SystemPresets {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "resources", "profiles", "BBL", "filament")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePreset(t, dir, "base.json",
		`{"name":"fdm_filament_pla","from":"system","filament_density":["1.24"],"filament_type":["PLA"]}`)
	writePreset(t, dir, "p2s.json",
		`{"name":"Bambu PLA Basic @BBL P2S","inherits":"fdm_filament_pla","setting_id":"GFSA00_11",
		  "instantiation":"true","filament_density":["1.26"],"compatible_printers":["Bambu Lab P2S 0.4 nozzle"]}`)
	sys, err := LoadSystemPresets(root, "filament")
	if err != nil {
		t.Fatal(err)
	}
	return sys
}

func TestResolveFilamentFlattensABundledFilamentBySettingIDOrName(t *testing.T) {
	sys := bundledFilaments(t)
	for _, ref := range []map[string]any{
		SystemFilamentRef("cloud", "GFSA00_11"),
		SystemFilamentRef("standard", "Bambu PLA Basic @BBL P2S"),
	} {
		got, err := sys.ResolveFilament(ref)
		if err != nil {
			t.Fatal(err)
		}
		if IsSystemFilamentRef(got) {
			t.Fatal("the reference must be replaced, not passed on")
		}
		d, _ := got["filament_density"].([]any)
		if len(d) != 1 || d[0] != "1.26" || got["filament_type"] == nil {
			t.Errorf("the nearest level wins and every level contributes: %v", got)
		}
		if got["inherits"] != "Bambu PLA Basic @BBL P2S" || got["setting_id"] != nil {
			t.Errorf("meta keys: inherits=%v setting_id=%v", got["inherits"], got["setting_id"])
		}
	}
	if _, err := sys.ResolveFilament(SystemFilamentRef("cloud", "GFSA99_99")); err == nil {
		t.Error("a filament this install does not ship must fail, not slice blank")
	}
	own := map[string]any{"name": "PLA copy"}
	if got, _ := sys.ResolveFilament(own); got["name"] != "PLA copy" {
		t.Error("a saved filament passes through unchanged")
	}
}
