package httpapi

// The colours a customer may pick on the personalisation link.
//
// The picker offered the shop's whole palette whatever was on the page, so a
// Royal diya holder - sold in purple, red and green - offered fifteen, and a
// customer could choose one that has never existed as a product.

import "testing"

// colourNames is the picker's list, as names.
func colourNames(out []publicColourResponse) []string {
	names := make([]string, 0, len(out))
	for _, c := range out {
		names = append(names, c.Name)
	}
	return names
}

// Every variant name the Diwali products carry has to resolve to a swatch, or
// the picker would silently drop it and fall back to the whole palette.
func TestTheDiwaliVariantNamesAreColours(t *testing.T) {
	for _, name := range []string{"PURPLE", "RED", "GREEN", "WHITE"} {
		if colourHexFor(name) == "" {
			t.Errorf("%q does not resolve to a swatch, so it would be dropped", name)
		}
	}
}

// A variant name that is NOT a colour must be dropped rather than offered: a
// customer picking it would hold their own job at "no swatch for ...", which
// looks accepted and stops the order.
func TestANonColourVariantNameIsNotOffered(t *testing.T) {
	for _, name := range []string{"2 hearts, With light", "No heart", "I NEED LIGHT + 390"} {
		if colourHexFor(name) != "" {
			t.Errorf("%q resolved to a swatch; it is not a colourway", name)
		}
	}
}
