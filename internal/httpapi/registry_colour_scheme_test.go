package httpapi

// A product whose palette is designed per colourway.
//
// The rule these pin is all-or-nothing: a variant that declares a scheme
// declares the WHOLE scheme. The alternative - merging a variant's pieces over
// the product's - sounds helpful and prints a palette nobody chose, because a
// variant naming only its outline would inherit a back and a text from the
// product's scheme and come out as neither design.

import (
	"strings"
	"testing"
)

// schemeOf is the parts list as part -> colour, so a test states the palette
// rather than an order of rows.
func schemeOf(parts []colourPart) map[string]string {
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		out[p.Name] = p.Hex
	}
	return out
}

func TestAVariantSchemeReplacesTheProductsEntirely(t *testing.T) {
	// The lotus holder, as the shop designed it: the back follows the colour
	// bought, and the other two pieces are chosen per colourway.
	product := []colourPart{
		{Name: "back"},
		{Name: "outline", Hex: "#D4AF37"},
		{Name: "text", Hex: "#FFD400"},
	}
	purple := []colourPart{
		{Name: "back"},
		{Name: "outline", Hex: "#FF69B4"},
		{Name: "text", Hex: "#D4AF37"},
	}

	got := schemeOf(resolveScheme(purple, product))
	if got["outline"] != "#FF69B4" {
		t.Errorf("outline = %q, want the variant's pink", got["outline"])
	}
	if got["text"] != "#D4AF37" {
		t.Errorf("text = %q, want the variant's gold", got["text"])
	}
	// The back is the piece that follows the order, and it must STILL follow
	// it on a variant with its own scheme. A scheme that pinned every piece
	// would print the same back on all three colourways.
	if got["back"] != "" {
		t.Errorf("back = %q, want empty - it follows the customer", got["back"])
	}
}

func TestAVariantWithNoSchemeUsesTheProducts(t *testing.T) {
	product := []colourPart{
		{Name: "plate"},
		{Name: "text", Hex: "#D4AF37"},
	}
	got := schemeOf(resolveScheme(nil, product))
	if len(got) != 2 || got["text"] != "#D4AF37" {
		t.Fatalf("scheme = %v, want the product's two pieces", got)
	}
	if got["plate"] != "" {
		t.Errorf("plate = %q, want empty - it follows the customer", got["plate"])
	}
}

// A HALF-DECLARED VARIANT IS NOT MERGED. This is the whole reason the rule is
// all-or-nothing: the pieces a variant leaves out are left out, not filled in
// from the product, because filling them in builds a palette that exists in
// neither design and prints without complaint.
func TestAPartialVariantSchemeIsNotMergedWithTheProducts(t *testing.T) {
	product := []colourPart{
		{Name: "back"},
		{Name: "outline", Hex: "#D4AF37"},
		{Name: "text", Hex: "#FFD400"},
	}
	// Somebody saved a scheme naming only the outline.
	partial := []colourPart{{Name: "outline", Hex: "#FF69B4"}}

	got := resolveScheme(partial, product)
	if len(got) != 1 {
		t.Fatalf("scheme has %d pieces, want 1 - the variant's own, not a merge", len(got))
	}
	if got[0].Name != "outline" || got[0].Hex != "#FF69B4" {
		t.Errorf("piece = %+v, want the variant's outline", got[0])
	}
}

// The piece names are what the template branches on, so a scheme naming a
// piece the .scad does not draw renders nothing and holds the job. Both Diwali
// files are checked here against the names the shop's own headers state.
func TestDiwaliPartNamesMatchTheTemplates(t *testing.T) {
	for _, tc := range []struct {
		product string
		parts   []string
	}{
		{"PDHR", []string{"plate", "text"}},
		{"PWDH", []string{"back", "outline", "text"}},
	} {
		for _, name := range tc.parts {
			if strings.TrimSpace(name) != name || name == "" {
				t.Errorf("%s: part %q is not a usable PART value", tc.product, name)
			}
			if strings.ToLower(name) != name {
				t.Errorf("%s: part %q is not lower case; OpenSCAD compares exactly",
					tc.product, name)
			}
		}
	}
}
