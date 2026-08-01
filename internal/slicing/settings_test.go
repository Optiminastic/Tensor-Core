package slicing

import (
	"slices"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestSliceSettingsNormalizeClampsAndAllowlists(t *testing.T) {
	got := SliceSettings{
		LayerHeightMM: ptr(0.9),      // above max -> clamped to 0.28
		WallLoops:     ptr(99),       // above max -> clamped to 8
		InfillPattern: ptr("GYROID"), // case-insensitive, in allowlist
		SupportDeg:    ptr(-5),       // below min -> clamped to 0
	}.Normalize()

	if got.LayerHeightMM == nil || *got.LayerHeightMM != maxLayerHeightMM {
		t.Errorf("layer height = %v, want %v", got.LayerHeightMM, maxLayerHeightMM)
	}
	if got.WallLoops == nil || *got.WallLoops != maxWallLoops {
		t.Errorf("wall loops = %v, want %d", got.WallLoops, maxWallLoops)
	}
	if got.InfillPattern == nil || *got.InfillPattern != "gyroid" {
		t.Errorf("infill pattern = %v, want gyroid", got.InfillPattern)
	}
	if got.SupportDeg == nil || *got.SupportDeg != minSupportDeg {
		t.Errorf("support deg = %v, want %d", got.SupportDeg, minSupportDeg)
	}
}

func TestSliceSettingsNormalizeDropsUnknownPattern(t *testing.T) {
	got := SliceSettings{InfillPattern: ptr("; rm -rf / --evil")}.Normalize()
	if got.InfillPattern != nil {
		t.Errorf("unknown pattern should be dropped, got %v", *got.InfillPattern)
	}
}

func TestSliceSettingsFlagsOnlyAllowlistedKeys(t *testing.T) {
	flags := SliceSettings{
		LayerHeightMM: ptr(0.16),
		WallLoops:     ptr(3),
		InfillPattern: ptr("grid"),
		SupportDeg:    ptr(25),
	}.Normalize().Flags()

	want := []string{
		"--layer-height=0.16",
		"--wall-loops=3",
		"--sparse-infill-pattern=grid",
		"--support-threshold-angle=25",
	}
	for _, w := range want {
		if !slices.Contains(flags, w) {
			t.Errorf("flags %v missing %q", flags, w)
		}
	}
}

func TestSupportEnabledDefaultsOn(t *testing.T) {
	if !(SliceSettings{}).SupportEnabled() {
		t.Error("support should default on")
	}
	if (SliceSettings{Support: ptr(false)}).SupportEnabled() {
		t.Error("support should be off when explicitly disabled")
	}
}
