package httpapi

// Giving a plate a slicer profile of its own, so a two-nozzle machine prints in
// two colours.
//
// THE PROBLEM. A plate Tensor builds carries two settings - filament_type and
// filament_colour - and nothing else, on the principle that the slicer
// configuration lives in BambuBuddy and duplicating it here is how two sources
// of truth start disagreeing. That principle is right and it still holds for
// every single-nozzle machine on the floor.
//
// It does not survive contact with an H2C. Measured against the live service,
// on the shop's own plate, with identical presets either side:
//
//	use_embedded_settings=false   enable_prime_tower 0   blue used_g 0.00
//	use_embedded_settings=true    enable_prime_tower 1   white 0.47g blue 0.32g
//
// BambuBuddy forces enable_prime_tower to 0 whenever it applies presets - over
// its own process preset, which says 1, and over an explicit process_override,
// which comes back 0 while its neighbours in the same block (prime_tower_width,
// flush_into_support) apply untouched. A tool change has nowhere to purge, so
// the slicer drops the second filament entirely, or emits purge moves off the
// bed and refuses the plate with "Found G-code in unprintable area of
// multi-extruder printers". Both symptoms, one cause.
//
// The nozzle map goes the same way. filament_map and filament_map_mode sent as
// process_overrides are discarded; written INTO the plate they produce
// filament_maps "2 1" - two different nozzles - and both filaments extrude.
//
// So on the embedded path the plate must carry a complete profile, and that is
// what this file is for. It is a copy, and copies rot: the template below is
// checked in rather than derived, because the expansion rules are not ours.
// A filament array is not "one entry per filament" - a 3-entry preset value
// becomes 8 for a 4-filament plate, a 4-entry one becomes 16, grouped by
// extruder and variant - and every attempt to synthesise those from the three
// presets segfaulted the slicer. BambuBuddy will not merge them either:
// /api/v1/slicer/preset-values flattens a single process preset and nothing
// more.
//
// What keeps it honest:
//   - Only the H2C path uses it. A2L and P2S keep the preset path unchanged.
//   - The keys the send path owns are stripped from the template at capture, so
//     it cannot carry a stale answer for a colour or a nozzle map.
//   - The printer and process presets in BambuBuddy stay authoritative for
//     everything else; this is a snapshot of what they already produce.
//
// Replace it with a fetch the day BambuBuddy can merge a profile itself.

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
)

// h2cProfile is a complete Bambu project profile for an H2C two-colour plate.
//
// Captured from Bambu Studio 02.08.02.61 saving the shop's own 115583-SKY_BLUE
// plank for a Bambu Lab H2C 0.4 nozzle on the "0.28mm heart" process, with two
// PLA filaments. Studio is the only thing that knows how to expand a profile
// correctly, which is why this is its output rather than our arithmetic.
//
//go:embed plateprofile/h2c.json
var h2cProfile []byte

// plateSettingsName is where a 3MF keeps its profile.
const plateSettingsName = "Metadata/project_settings.config"

// embedPlateProfile rewrites a plate's project_settings.config to the full
// profile, with the per-plate values written in.
//
// Returns the plate unchanged, and false, when there is nothing to do - a
// single-colour bed, or a machine with one nozzle. Those slice correctly on the
// preset path today and must keep doing so: this swaps a profile the shop
// maintains in BambuBuddy for one checked in here, and that trade is only worth
// making where the preset path cannot work at all.
func embedPlateProfile(plate []byte, colours []string, nozzleMap map[string]any) ([]byte, bool, error) {
	if len(nozzleMap) == 0 || len(colours) < 2 {
		return plate, false, nil
	}

	var profile map[string]any
	if err := json.Unmarshal(h2cProfile, &profile); err != nil {
		return nil, false, fmt.Errorf("read the embedded H2C profile: %w", err)
	}

	// The plate's own answers, over the template's.
	profile["filament_colour"] = colours
	profile["filament_type"] = repeatString("PLA", len(colours))
	profile["filament_settings_id"] = repeatString("Bambu PLA Basic @BBL H2C", len(colours))
	// A two-colour plate must purge somewhere. This is the key BambuBuddy
	// refuses on the preset path and the whole reason for the embedded one.
	profile["enable_prime_tower"] = "1"
	// filament_map, filament_map_mode and extruder_ams_count, exactly as
	// nozzleMapOverrides computed them for THIS machine.
	for k, v := range nozzleMap {
		profile[k] = v
	}

	settings, err := json.Marshal(profile)
	if err != nil {
		return nil, false, fmt.Errorf("write the plate profile: %w", err)
	}

	rewritten, err := replaceZipEntry(plate, plateSettingsName, settings)
	if err != nil {
		return nil, false, err
	}
	return rewritten, true, nil
}

func repeatString(v string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// replaceZipEntry rewrites one file inside a zip, leaving every other entry
// byte-for-byte as it was.
//
// A 3MF is a zip and the slicer reads several of its members - the model, the
// per-object settings, the relationships. Rebuilding it from scratch would risk
// all of them to change one, so everything else is copied across untouched. An
// entry that is not present is added, because a plate written before this
// carried no profile at all.
func replaceZipEntry(archive []byte, name string, content []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read the plate: %w", err)
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	replaced := false
	for _, f := range zr.File {
		if f.Name == name {
			if err := writeZipFile(zw, name, content); err != nil {
				return nil, err
			}
			replaced = true
			continue
		}
		if err := copyZipFile(zw, f); err != nil {
			return nil, err
		}
	}
	if !replaced {
		if err := writeZipFile(zw, name, content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finish the plate: %w", err)
	}
	return out.Bytes(), nil
}

func writeZipFile(zw *zip.Writer, name string, content []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}
	if _, err := w.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func copyZipFile(zw *zip.Writer, f *zip.File) error {
	r, err := f.Open()
	if err != nil {
		return fmt.Errorf("read %s from the plate: %w", f.Name, err)
	}
	defer func() { _ = r.Close() }()

	// The original header, so compression and timestamps survive the rewrite.
	header := f.FileHeader
	w, err := zw.CreateHeader(&header)
	if err != nil {
		return fmt.Errorf("copy %s: %w", f.Name, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copy %s: %w", f.Name, err)
	}
	return nil
}
