package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// This decides whether an order flows through untouched or waits for a designer.
// Both DNP families in the live data are covered: T3DPS-DNP-1..16 and DNPLB-*.
func TestIsGeneratedProduct(t *testing.T) {
	for _, c := range []struct {
		name string
		sku  string
		prod string
		want bool
	}{
		{"dnp sku", "T3DPS-DNP-9", "", true},
		{"dnp sku, high index", "T3DPS-DNP-16", "", true},
		{"lightbox family", "DNPLB-2", "", true},
		// The storefront is not consistent about case.
		{"lower case sku", "t3dps-dnp-11", "", true},
		{"ordinary product", "THO-PLA-FFF-0022", "Sunrise Lamp", false},
		{"nothing at all", "", "", false},

		// The letters appearing inside another word is not the product family.
		// A false positive here holds an order that had nothing wrong with it,
		// so the match is per hyphen-separated segment rather than a substring.
		{"substring in a sku segment", "CARDNPACK-1", "", false},
		{"substring, no hyphens", "GRANDNPRIX", "", false},

		// Nine live plank lines - the GREEN and PURPLE variants - carry every
		// STEP property and no SKU at all. They are planks; the variant just
		// lost its SKU in the catalogue. Without the name fallback they were
		// filed as manual-upload work and never rendered.
		{"plank with no sku", "", "Dual Name Plank", true},
		{"plank, name decorated", "", "DUAL NAME PLANK - Green", true},
		{"plank with both", "T3DPS-DNP-9", "Dual Name Plank", true},

		// The name must be as tight as the SKU rule. These are genuinely
		// different products that happen to sit next to planks in the store.
		{"photo frame", "", "DUAL NAME & PHOTO FRAME", false},
		{"love display", "", "LOVE DISPLAY", false},
		{"temple name plate", "", "TEMPLE - Home Name Plate", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := IsGeneratedProduct(c.sku, c.prod); got != c.want {
				t.Errorf("IsGeneratedProduct(%q, %q) = %v, want %v",
					c.sku, c.prod, got, c.want)
			}
		})
	}
}

// Which jobs wait for a person and which wait for a render. Getting this
// backwards is not cosmetic: a plank marked "approval required" sits until
// somebody uploads a file it was going to build itself, and an ordinary
// product marked "generating" waits forever for a render that never runs.
func TestModelStatusOf(t *testing.T) {
	fileID := uuid.New()
	sku := func(s string) *string { return &s }

	for _, c := range []struct {
		name string
		job  gen.ProductionJob
		want string
	}{
		{
			"has a model already",
			gen.ProductionJob{PrintFileID: &fileID, Sku: sku("THO-PLA-FFF-0022")},
			ModelReady,
		},
		{
			// A plank builds itself; nobody needs to be asked.
			"plank awaiting its render",
			gen.ProductionJob{Sku: sku("T3DPS-DNP-9")},
			ModelGenerating,
		},
		{
			"other product with no model",
			gen.ProductionJob{Sku: sku("THO-PLA-FFF-0022")},
			ModelApprovalRequired,
		},
		{
			// No SKU is nothing Tensor can render, so it needs a person.
			"no sku at all",
			gen.ProductionJob{},
			ModelApprovalRequired,
		},
		{
			// A plank that already has its model is ready, not generating.
			"plank with its render attached",
			gen.ProductionJob{PrintFileID: &fileID, Sku: sku("T3DPS-DNP-9")},
			ModelReady,
		},
		{
			// A failed render is not "in progress". Reporting it as such would
			// leave the job looking busy for ever, which is the exact state
			// this distinction exists to end.
			"plank whose render failed",
			gen.ProductionJob{Sku: sku("T3DPS-DNP-9"), ModelError: sku("openscad: exit 1")},
			ModelFailed,
		},
		{
			// A retry that worked, or a hand-uploaded file: the model is here,
			// so the old error no longer describes the job.
			"plank that failed then got a model",
			gen.ProductionJob{
				PrintFileID: &fileID, Sku: sku("T3DPS-DNP-9"), ModelError: sku("openscad: exit 1"),
			},
			ModelReady,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := modelStatusOf(c.job); got != c.want {
				t.Errorf("modelStatusOf = %q, want %q", got, c.want)
			}
		})
	}
}

// The store sells the colour and the light option as one string, and only the
// colour decides which filament goes in the printer - the light is a
// fulfilment extra. Colour is also what the planner groups beds by, so a plank
// arriving without one cannot be batched sensibly.
func TestColourFromVariant(t *testing.T) {
	v := func(s string) *string { return &s }
	for _, c := range []struct {
		in   *string
		want string
	}{
		{v("SKY BLUE / NO LIGHT"), "SKY BLUE"},
		{v("RED / I NEED LIGHT + ₹390"), "RED"},
		{v("BABY PINK / NO LIGHT"), "BABY PINK"},
		{v("WHITE"), "WHITE"},
		{v("  GOLD  /  NO LIGHT "), "GOLD"},
		{v(""), ""},
		{nil, ""},
	} {
		got := colourFromVariant(c.in)
		if got != c.want {
			in := "<nil>"
			if c.in != nil {
				in = *c.in
			}
			t.Errorf("colourFromVariant(%q) = %q, want %q", in, got, c.want)
		}
	}
}

// The customer's colour choice is the one customisation on a plank that cannot
// be corrected after printing, so it must never be guessed.
func TestNormaliseHex(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"#1A1A1A", "#1A1A1A", true},
		{"1a1a1a", "#1A1A1A", true},    // BambuBuddy sometimes omits the hash
		{"#1A1A1AFF", "#1A1A1A", true}, // and sometimes carries alpha
		{"  #87CEEB  ", "#87CEEB", true},
		{"", "", false},
		{"#FFF", "", false},
		{"white", "", false},
		{"#GGGGGG", "", false},
	} {
		got, ok := normaliseHex(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("normaliseHex(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Every colour seen in a real order must resolve to something, or a plank the
// shop plainly sells fails to build. The filament shelf covers most; these fill
// the gaps when stock is momentarily out.
func TestFallbackCoversOrderedColours(t *testing.T) {
	// Colours absent from the filament shelf at the time of writing.
	for _, colour := range []string{"WHITE", "RED", "BABY PINK"} {
		if _, ok := fallbackColours[strings.ToLower(colour)]; !ok {
			t.Errorf("%q was ordered but has no swatch on the shelf or here", colour)
		}
	}
	for name, hex := range fallbackColours {
		if _, ok := normaliseHex(hex); !ok {
			t.Errorf("fallback %q has an invalid hex %q", name, hex)
		}
	}
}
