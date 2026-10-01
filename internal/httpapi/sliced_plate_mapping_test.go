package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

func slicedOf(hexes ...string) []bambubuddy.SlicedFilament {
	out := make([]bambubuddy.SlicedFilament, 0, len(hexes))
	for i, h := range hexes {
		out = append(out, bambubuddy.SlicedFilament{ID: i + 1, Colour: h, Type: "PLA", UsedForObject: true})
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The failure this whole file exists for: the bed was bound across four slots,
// the slicer produced a plate declaring two, and the four-entry mapping halted
// H2 and H3 at their first tool change. Two entries printed.
func TestMappingForSlicedPlateShrinksToWhatTheSlicerProduced(t *testing.T) {
	sliced := slicedOf("#FFFFFF", "#FFF144")
	mapping := []int{-1, 3, 0, 2}
	hexes := []string{"#FFFFFF", "#FFF144", "#F72323", "#F98C36"}

	got, err := mappingForSlicedPlate(sliced, mapping, hexes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{-1, 3}; !equalInts(got, want) {
		t.Fatalf("mapping = %v, want %v", got, want)
	}
}

// The external spool stays -1. Proved on H3: a two-entry mapping carrying -1
// loaded tray 254 and printed, so the printer resolves the sentinel itself and
// Tensor must not "helpfully" send 254 instead.
func TestMappingForSlicedPlateKeepsTheExternalSpoolSentinel(t *testing.T) {
	got, err := mappingForSlicedPlate(slicedOf("#FFFFFF"), []int{-1, 3}, []string{"#FFFFFF", "#FFF144"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{-1}; !equalInts(got, want) {
		t.Fatalf("mapping = %v, want %v", got, want)
	}
}

// The slicer renumbers what it keeps, so position cannot be trusted: the
// plate's filament 1 here is the bed's slot 2.
func TestMappingForSlicedPlateFollowsColourNotPosition(t *testing.T) {
	got, err := mappingForSlicedPlate(
		slicedOf("#FFF144", "#FFFFFF"), []int{-1, 3}, []string{"#FFFFFF", "#FFF144"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{3, -1}; !equalInts(got, want) {
		t.Fatalf("mapping = %v, want %v", got, want)
	}
}

// Two spools of one colour must not both bind to the plate's single slot.
func TestMappingForSlicedPlateDoesNotReuseOneTrayTwice(t *testing.T) {
	got, err := mappingForSlicedPlate(
		slicedOf("#FFFFFF", "#FFFFFF"), []int{1, 2}, []string{"#FFFFFF", "#FFFFFF"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("mapping = %v, want %v", got, want)
	}
}

// A colour the bed holds no spool for, on a plate that is ALSO the wrong
// length, is a real mismatch: refuse rather than print the lettering in the
// body colour.
func TestMappingForSlicedPlateRefusesAnUnmatchedColour(t *testing.T) {
	_, err := mappingForSlicedPlate(
		slicedOf("#FFFFFF", "#1560BD"), []int{-1, 3, 0}, []string{"#FFFFFF", "#FFF144", "#F72323"})
	if err == nil {
		t.Fatal("expected a refusal when the sliced plate needs a colour no spool holds")
	}
}

// Same length and no colour match means the slicer rewrote a hex rather than
// dropping a filament. Every single-colour bed has always shipped the planned
// mapping, so that is what goes - failing here would ground beds that print.
func TestMappingForSlicedPlateFallsBackWhenLengthsAlreadyAgree(t *testing.T) {
	mapping := []int{7}
	got, err := mappingForSlicedPlate(slicedOf("#ABCDEF"), mapping, []string{"#FFFFFF"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !equalInts(got, mapping) {
		t.Fatalf("mapping = %v, want the planned %v", got, mapping)
	}
}

func TestMappingForSlicedPlateRefusesAnEmptyPlate(t *testing.T) {
	if _, err := mappingForSlicedPlate(nil, []int{1}, []string{"#FFFFFF"}); err == nil {
		t.Fatal("expected a refusal for a plate declaring no filament")
	}
}
