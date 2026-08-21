package slicing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResolveProfilesAgainstRealInstall is the test that would have caught the
// bug this file's package comment describes: machineProfile was
// "Bambu Lab H2C 0.4 & 0.4 nozzles", a Bambu Studio *UI* label with no
// corresponding bundled file, so every real slice failed at ResolveProfiles
// and only FAKE_SLICE hid it.
//
// It runs only where a real Bambu Studio install exists (BAMBU_ROOT, i.e.
// inside the slice worker image), because that is the only place the question
// can actually be answered - a fixture of filenames we invent ourselves would
// happily agree with whatever we wrote in profiles.go.
//
// Run it against the image with:
//
//	docker run --rm -e BAMBU_ROOT=/opt/bambu/squashfs-root tensor-slice-worker \
//	  go test ./internal/slicing/ -run RealInstall
func TestResolveProfilesAgainstRealInstall(t *testing.T) {
	root := os.Getenv("BAMBU_ROOT")
	if root == "" {
		t.Skip("BAMBU_ROOT is not set; no Bambu Studio install to resolve against")
	}
	if _, err := os.Stat(filepath.Join(root, "resources", "profiles", "BBL")); err != nil {
		t.Skipf("BAMBU_ROOT=%s has no bundled BBL profiles: %v", root, err)
	}

	// Every combination the design form can produce must resolve. A material
	// or quality that silently has no preset is a design that can never be
	// costed.
	for material := range materialFilament {
		for quality := range qualityProcess {
			got, err := ResolveProfiles(root, material, quality)
			if err != nil {
				t.Errorf("ResolveProfiles(%q, %q) = %v; every dropdown combination must map to a bundled preset",
					material, quality, err)
				continue
			}
			if got.DensityGCm3 <= 0 {
				t.Errorf("ResolveProfiles(%q, %q) resolved density %v; weight is derived from length x density, so a zero density silently zeroes every filament cost",
					material, quality, got.DensityGCm3)
			}
		}
	}
}

// TestRunSliceAgainstRealInstall drives the real thing: ResolveProfiles into
// RunSlice into the parsers, against a genuine model. It is the only test that
// exercises bambu.go's actual argument list, which is where the H2C's
// "some filaments can not be mapped ... for multi extruder printer" (return
// -66) surfaced - a failure no amount of filename checking can see.
//
// Needs both a Bambu install and a model, so it runs only in the slice worker
// image:
//
//	go test -c -o slicing.test ./internal/slicing/
//	docker run --rm -v "$PWD:/t:ro" -v "/path/to/stl_files:/models:ro" \
//	  -e BAMBU_ROOT=/opt/bambu/squashfs-root -e SLICE_TEST_MODEL=/models/boxopener.stl \
//	  tensor-slice-worker /t/slicing.test -test.run RunSliceAgainstRealInstall -test.v
func TestRunSliceAgainstRealInstall(t *testing.T) {
	root := os.Getenv("BAMBU_ROOT")
	model := os.Getenv("SLICE_TEST_MODEL")
	if root == "" || model == "" {
		t.Skip("BAMBU_ROOT and SLICE_TEST_MODEL must both be set to slice for real")
	}

	profiles, err := ResolveProfiles(root, "PLA Basics", "0.20-standard")
	if err != nil {
		t.Fatalf("ResolveProfiles: %v", err)
	}
	outdir := t.TempDir()
	out, err := RunSlice(t.Context(), root, profiles, model, 15, 1, outdir, 10*time.Minute)
	if err != nil {
		t.Fatalf("RunSlice: %v", err)
	}

	result, err := LoadResultJSON(out.ResultJSONPath)
	if err != nil {
		t.Fatalf("LoadResultJSON: %v", err)
	}
	sliceInfo, err := LoadSliceInfo(out.Gcode3mfPath)
	if err != nil {
		t.Fatalf("LoadSliceInfo: %v", err)
	}
	plateGcode, err := LoadPlateGcode(out.Gcode3mfPath)
	if err != nil {
		t.Fatalf("LoadPlateGcode: %v", err)
	}
	metrics, err := ExtractMetrics(result, sliceInfo, profiles.DensityGCm3, plateGcode)
	if err != nil {
		t.Fatalf("ExtractMetrics: %v", err)
	}

	// The slicer reports grams as 0 on these presets, so a zero here means the
	// density derivation in gcode.go silently stopped working - which would
	// zero every filament cost downstream without failing anything.
	if metrics.PrintTimeS <= 0 {
		t.Errorf("print time = %vs; a real slice must produce a positive time (result.json's sliced_plates[].total_predication)", metrics.PrintTimeS)
	}
	if metrics.FilamentWeightG <= 0 {
		t.Errorf("filament = %v g; weight is derived from length x density, so zero means that derivation broke", metrics.FilamentWeightG)
	}
	t.Logf("sliced %s: %.1fs (%.2f min), %.2f g, %.2f mm, support=%v",
		model, metrics.PrintTimeS, metrics.PrintTimeS/60, metrics.FilamentWeightG,
		metrics.FilamentLengthMM, metrics.SupportUsed)
}

// TestPresetNamesCarryTheMachineSuffix pins the naming scheme itself, with no
// filesystem involved: a process or filament preset is only valid for the
// machine it is suffixed for, so dropping the suffix (or letting it drift from
// machineProfile's model) produces a pair Bambu will not accept together.
func TestPresetNamesCarryTheMachineSuffix(t *testing.T) {
	for quality, process := range qualityProcess {
		if got, want := presetName(process), process+" "+presetSuffix; got != want {
			t.Errorf("presetName(%q) = %q, want %q", quality, got, want)
		}
	}
	for _, f := range AvailableFilaments() {
		if len(f.FilamentPreset) <= len(presetSuffix) ||
			f.FilamentPreset[len(f.FilamentPreset)-len(presetSuffix):] != presetSuffix {
			t.Errorf("AvailableFilaments() advertises %q, which does not carry the %q suffix; the machine-admin catalogue would show a preset the slicer never loads",
				f.FilamentPreset, presetSuffix)
		}
	}
}

// TestLegacyQualityAliasesResolve covers the shorthand values older designs
// carry. Without the aliases these fail with "unknown quality", which does not
// merely skip a slice - the batch never gets a measured print time, so it can
// never be committed to a machine and its work silently stops moving.
func TestLegacyQualityAliasesResolve(t *testing.T) {
	for legacy, want := range qualityAliases {
		if _, ok := qualityProcess[want]; !ok {
			t.Errorf("alias %q points at %q, which is not a known quality key", legacy, want)
		}
		if got := canonicalQuality(legacy); got != want {
			t.Errorf("canonicalQuality(%q) = %q, want %q", legacy, got, want)
		}
		if LayerHeightMM(legacy) == nil {
			t.Errorf("LayerHeightMM(%q) = nil; a legacy quality must still yield a layer height", legacy)
		}
	}
	// A current key must pass through untouched.
	if got := canonicalQuality("0.20-standard"); got != "0.20-standard" {
		t.Errorf("canonicalQuality rewrote a current key to %q", got)
	}
}
