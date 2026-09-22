package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

func catColour(brand, name, hex string, isDefault bool) bambubuddy.CatalogueColour {
	return bambubuddy.CatalogueColour{
		Manufacturer: brand, ColorName: name, HexColor: hex, IsDefault: isDefault,
	}
}

// The case that makes the whole idea worth having: an AMS reports #D3B7A7 and
// nothing else - no sub-brand, filament id GFL99 - so without the catalogue
// there is no name anywhere, and the nearest colour Tensor knows is a guess.
func TestCatalogueByHexNamesASpoolTheAMSCannot(t *testing.T) {
	got := catalogueByHex([]bambubuddy.CatalogueColour{
		catColour("Bambu Lab", "Latte Brown", "#D3B7A7", true),
	})
	if got["#D3B7A7"].Name != "Latte Brown" {
		t.Fatalf("#D3B7A7 = %q, want Latte Brown", got["#D3B7A7"].Name)
	}
	if got["#D3B7A7"].Brand != "Bambu Lab" {
		t.Errorf("brand = %q, want Bambu Lab - the suggestion is only judgeable "+
			"if you can see who is making it", got["#D3B7A7"].Brand)
	}
}

// White is three names on this catalogue. Whichever is chosen, it must be the
// same one every time: a suggestion that changed between page loads would look
// like the fleet had changed.
func TestCatalogueByHexIsStableWhenOneHexHasSeveralNames(t *testing.T) {
	colours := []bambubuddy.CatalogueColour{
		catColour("Hatchbox", "Pristine White", "#FFFFFF", false),
		catColour("Bambu Lab", "Jade White", "#FFFFFF", false),
		catColour("Bambu Lab", "Ivory White", "#FFFFFF", false),
	}
	first := catalogueByHex(colours)["#FFFFFF"].Name
	for i := 0; i < 20; i++ {
		if got := catalogueByHex(colours)["#FFFFFF"].Name; got != first {
			t.Fatalf("#FFFFFF suggested %q then %q", first, got)
		}
	}
	if first != "Ivory White" {
		t.Errorf("#FFFFFF = %q, want the alphabetically first of the tied names", first)
	}
}

// A manufacturer's default entry is the one that colour normally ships as.
func TestCatalogueByHexPrefersTheDefaultEntry(t *testing.T) {
	got := catalogueByHex([]bambubuddy.CatalogueColour{
		catColour("Hatchbox", "Charcoal", "#000000", false),
		catColour("Bambu Lab", "Black", "#000000", true),
	})
	if got["#000000"].Name != "Black" {
		t.Errorf("#000000 = %q, want Black (the default entry)", got["#000000"].Name)
	}
}

func TestCatalogueByHexSkipsUnusableRows(t *testing.T) {
	got := catalogueByHex([]bambubuddy.CatalogueColour{
		catColour("Bambu Lab", "", "#123456", true),           // no name
		catColour("Bambu Lab", "Nameless", "not-a-hex", true), // no colour
		catColour("Bambu Lab", "  ", "#ABCDEF", true),         // blank name
	})
	if len(got) != 0 {
		t.Errorf("catalogueByHex = %v, want nothing usable", got)
	}
}

// Hex casing and an alpha suffix both occur in the wild; the map is keyed the
// way normaliseHex writes them or nothing ever matches a tray.
func TestCatalogueByHexNormalisesItsKeys(t *testing.T) {
	got := catalogueByHex([]bambubuddy.CatalogueColour{
		catColour("Bambu Lab", "Cyan", "0086d6ff", true),
	})
	if got["#0086D6"].Name != "Cyan" {
		t.Errorf("lowercase hex with alpha did not index as #0086D6: %v", got)
	}
}
