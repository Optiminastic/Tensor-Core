package httpapi

// What a plate must carry before a two-nozzle machine will print it in two
// colours, and what it must NOT carry anywhere else.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
)

// plateZip is a minimal 3MF: a model, per-object settings, and the two-key
// profile meshio writes today.
func plateZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"3D/3dmodel.model":                 "<model/>",
		"Metadata/model_settings.config":   "<config/>",
		"Metadata/project_settings.config": `{"filament_type":["PLA","PLA"],"filament_colour":["#FFFFFF","#2850E0"]}`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("build plate: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("build plate: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("build plate: %v", err)
	}
	return buf.Bytes()
}

func settingsOf(t *testing.T, plate []byte) map[string]any {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(plate), int64(len(plate)))
	if err != nil {
		t.Fatalf("read plate: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != plateSettingsName {
			continue
		}
		r, err := f.Open()
		if err != nil {
			t.Fatalf("open settings: %v", err)
		}
		defer func() { _ = r.Close() }()
		var out map[string]any
		if err := json.NewDecoder(r).Decode(&out); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		return out
	}
	t.Fatal("the plate has no project_settings.config")
	return nil
}

// The whole point: a two-colour plate bound for two nozzles carries a complete
// profile, with the prime tower on and the nozzle map in it.
//
// Both of those are keys BambuBuddy refuses on the preset path - it resets
// enable_prime_tower to 0 over its own preset, and discards filament_map sent
// as a process override. Measured on the shop's own plank: 0.00g of the second
// colour that way, 27.05g this way.
func TestATwoColourPlateForTwoNozzlesCarriesItsOwnProfile(t *testing.T) {
	nozzleMap := map[string]any{
		"filament_map_mode":  "Manual",
		"filament_map":       []string{"1", "2"},
		"extruder_ams_count": []string{"1#1|4#0", "1#0|4#1"},
	}
	colours := []string{"#FFFFFF", "#0086D6"}

	out, embedded, err := embedPlateProfile(plateZip(t), colours, nozzleMap)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !embedded {
		t.Fatal("a two-colour plate on a two-nozzle machine got no profile")
	}

	got := settingsOf(t, out)
	if len(got) < 500 {
		t.Errorf("profile has %d keys; a plate sliced on the embedded path needs the "+
			"whole thing, not the two meshio writes", len(got))
	}
	if got["enable_prime_tower"] != "1" {
		t.Errorf("enable_prime_tower = %v, want \"1\" - without it the tool change has "+
			"nowhere to purge and the second filament is dropped", got["enable_prime_tower"])
	}
	if got["filament_map_mode"] != "Manual" {
		t.Errorf("filament_map_mode = %v, want Manual", got["filament_map_mode"])
	}
	// The plate's own colours, not the template's.
	var gotColours []string
	for _, c := range got["filament_colour"].([]any) {
		gotColours = append(gotColours, c.(string))
	}
	if len(gotColours) != 2 || gotColours[0] != "#FFFFFF" || gotColours[1] != "#0086D6" {
		t.Errorf("filament_colour = %v, want this plate's colours", gotColours)
	}
	// Every filament-indexed array the send path owns must match the colour count.
	for _, k := range []string{"filament_type", "filament_settings_id"} {
		if n := len(got[k].([]any)); n != len(colours) {
			t.Errorf("%s has %d entries, want %d - a mismatch here segfaults the slicer",
				k, n, len(colours))
		}
	}
}

// One nozzle, or one colour, and the plate is left exactly as it was.
//
// Those beds slice correctly on the preset path today and must keep doing so.
// The embedded path trades a profile the shop maintains in BambuBuddy for one
// checked into this repo, and that is only worth doing where the preset path
// cannot work at all.
func TestASingleNozzleOrSingleColourPlateIsLeftAlone(t *testing.T) {
	plate := plateZip(t)
	twoColours := []string{"#FFFFFF", "#2850E0"}

	for _, c := range []struct {
		name      string
		colours   []string
		nozzleMap map[string]any
	}{
		// A2L and P2S: nozzleMapOverrides returns nil for one nozzle.
		{"one nozzle", twoColours, nil},
		// A bed of one colour never changes filament, so it never purges.
		{"one colour", []string{"#FFFFFF"}, map[string]any{"filament_map": []string{"1"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, embedded, err := embedPlateProfile(plate, c.colours, c.nozzleMap)
			if err != nil {
				t.Fatalf("embed: %v", err)
			}
			if embedded {
				t.Error("a plate that slices fine on the preset path was given an embedded profile")
			}
			if !bytes.Equal(out, plate) {
				t.Error("the plate was rewritten when it should have been passed through untouched")
			}
			if n := len(settingsOf(t, out)); n != 2 {
				t.Errorf("project_settings has %d keys, want the original 2", n)
			}
		})
	}
}

// Rewriting the profile must not disturb the rest of the 3MF.
//
// A 3MF is a zip the slicer reads several members of - the model, the
// per-object extruder assignment, the relationships. Losing any of them to
// change one is how a plate that used to slice stops slicing.
func TestRewritingTheProfileLeavesEveryOtherEntryIntact(t *testing.T) {
	plate := plateZip(t)
	out, _, err := embedPlateProfile(plate, []string{"#FFFFFF", "#2850E0"},
		map[string]any{"filament_map": []string{"1", "2"}})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}

	before, after := entriesOf(t, plate), entriesOf(t, out)
	if len(before) != len(after) {
		t.Fatalf("entries: %d -> %d; the plate lost or gained a member", len(before), len(after))
	}
	for name, body := range before {
		if name == plateSettingsName {
			continue
		}
		if after[name] != body {
			t.Errorf("%s changed while rewriting the profile", name)
		}
	}
}

// A plate written before this change carries no project_settings at all; it
// must gain one rather than be rejected.
func TestAPlateWithNoProfileGainsOne(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("3D/3dmodel.model")
	_, _ = w.Write([]byte("<model/>"))
	_ = zw.Close()

	out, embedded, err := embedPlateProfile(buf.Bytes(), []string{"#FFFFFF", "#2850E0"},
		map[string]any{"filament_map": []string{"1", "2"}})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !embedded {
		t.Fatal("a plate with no profile was not given one")
	}
	if got := settingsOf(t, out); len(got) < 500 {
		t.Errorf("profile has %d keys, want the whole thing", len(got))
	}
}

func entriesOf(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		var b bytes.Buffer
		if _, err := b.ReadFrom(r); err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		_ = r.Close()
		out[f.Name] = b.String()
	}
	return out
}
