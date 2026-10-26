package httpapi

// Which colourway's palette a job prints in.
//
// A per-variant scheme IS a colourway - the Lotus is purple-with-pink, or
// red-with-gold, or white-with-gold. Choosing it by SKU was right only while
// nobody could change their mind, and the personalisation link exists so they
// can.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func variantRow(name, status string) gen.ListVariantsForProductRow {
	return gen.ListVariantsForProductRow{ID: uuid.New(), Name: name, Status: status}
}

// THE BUG THIS FIXES, exactly as it happened: a customer bought PWDH-P and
// chose WHITE on the link. The scheme was looked up by SKU, so the model came
// back purple-and-pink with a gold name on it, and nothing disagreed.
func TestTheChosenColourPicksTheColourway(t *testing.T) {
	purple := variantRow("Purple", "active")
	red := variantRow("Red", "active")
	white := variantRow("White", "active")
	variants := []gen.ListVariantsForProductRow{purple, red, white}

	got := matchVariantByColour(variants, "WHITE")
	if got == nil || *got != white.ID {
		t.Fatalf("choosing WHITE did not reach the White colourway")
	}
	if got != nil && *got == purple.ID {
		t.Error("the bought variant's scheme was used instead of the chosen one")
	}
}

// Canonicalised on both sides: the store sends "Gold" and "GOLD" for one
// filament, and a case mismatch would silently fall back to the SKU.
func TestColourwayMatchingIgnoresCase(t *testing.T) {
	v := []gen.ListVariantsForProductRow{variantRow("Purple", "active")}
	if got := matchVariantByColour(v, "purple"); got == nil {
		t.Error("lower case did not match")
	}
	if got := matchVariantByColour(v, "  PURPLE  "); got == nil {
		t.Error("surrounding space did not match")
	}
}

// A retired colourway is not offered and must not be rendered: its scheme
// describes a product the shop has stopped selling.
func TestARetiredColourwayIsNotChosen(t *testing.T) {
	v := []gen.ListVariantsForProductRow{variantRow("Gold", "retired")}
	if got := matchVariantByColour(v, "GOLD"); got != nil {
		t.Error("a retired variant's scheme was selected")
	}
}

// A colour this product has no variant for falls through, so the caller can
// keep the bought variant's scheme rather than rendering nothing.
func TestAColourWithNoVariantFallsThrough(t *testing.T) {
	v := []gen.ListVariantsForProductRow{variantRow("Purple", "active")}
	if got := matchVariantByColour(v, "TURQUOISE"); got != nil {
		t.Error("a colour this product does not sell matched a variant")
	}
	if got := matchVariantByColour(v, ""); got != nil {
		t.Error("an empty colour matched a variant")
	}
}
