package personalise

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func props(pairs ...string) []production.LineProp {
	out := make([]production.LineProp, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, production.LineProp{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// The property names are the storefront's real ones, punctuation and all, taken
// from live orders: "STEP 3-", "STEP 4-First Name-", "STEP 5-Second Name".
func liveOrder(hearts string) []production.LineProp {
	return props(
		"_has_gpo", "622778",
		"STEP 3-", hearts,
		"STEP 4-First Name-", "VASU",
		"STEP 5-Second Name", "PADMANABH",
		"STEP 6 - WhatsApp Number", "8709192686",
		"_gpo_addon_price", "30",
	)
}

// The heart count picks the template, and the three templates are not
// interchangeable: they differ in margins (40/40, 9/35, 9/9), plate thickness
// and whether the Fusion shear is applied. Rendering the wrong one produces a
// plank that is wrong in the hand, not merely different on screen.
func TestTemplateChosenByHeartCount(t *testing.T) {
	for _, c := range []struct {
		value string
		want  string
		heart int
	}{
		{"2 RED HEART", templateTwoHeart, 2},
		{"1 RED HEART", templateOneHeart, 1},
		// The store may reword its options; a number anywhere still decides.
		{"2 PINK HEARTS", templateTwoHeart, 2},
		{"NO HEART", templateNoHeart, 0},
		{"none", templateNoHeart, 0},
		{"0 HEART", templateNoHeart, 0},
	} {
		t.Run(c.value, func(t *testing.T) {
			p, err := ParamsFromProperties(liveOrder(c.value))
			if err != nil {
				t.Fatalf("ParamsFromProperties(%q): %v", c.value, err)
			}
			if p.Template != c.want {
				t.Errorf("template = %q, want %q", p.Template, c.want)
			}
			if p.Hearts != c.heart {
				t.Errorf("hearts = %d, want %d", p.Hearts, c.heart)
			}
		})
	}
}

// A missing heart option must not silently become zero. "The customer chose
// none" and "the property did not import" are indistinguishable here, and
// guessing wrong prints 9mm margins where 40mm were ordered. Right now 43 of
// the 46 imported orders carry no properties at all, so this is the common
// case, not a corner one.
func TestMissingHeartOptionIsAnError(t *testing.T) {
	p := props("STEP 4-First Name-", "VASU", "STEP 5-Second Name", "PADMANABH")
	if _, err := ParamsFromProperties(p); err == nil {
		t.Fatal("an order with no heart option must not be rendered")
	}
}

func TestBothNamesRequired(t *testing.T) {
	for _, c := range []struct {
		name string
		p    []production.LineProp
	}{
		{"no second name", props("STEP 3-", "1 RED HEART", "STEP 4-First Name-", "VASU")},
		{"no first name", props("STEP 3-", "1 RED HEART", "STEP 5-Second Name", "REYA")},
		{"blank first name", props("STEP 3-", "1 RED HEART", "STEP 4-First Name-", "   ",
			"STEP 5-Second Name", "REYA")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParamsFromProperties(c.p); err == nil {
				t.Error("a plank with one name is not a dual name plank")
			}
		})
	}
}

// Past MAX_LETTERS the glyph run is squeezed until the lettering stops reading
// from either side, which is the entire product. Holding beats printing it.
func TestOverlongNameIsRejected(t *testing.T) {
	p := props(
		"STEP 3-", "1 RED HEART",
		"STEP 4-First Name-", "BARTHOLOMEWXX", // 13
		"STEP 5-Second Name", "REYA",
	)
	_, err := ParamsFromProperties(p)
	if err == nil {
		t.Fatal("a 13-letter name must be rejected")
	}
	if !strings.Contains(err.Error(), "13") {
		t.Errorf("the error should say how long the name was, got %q", err)
	}
}

func TestNamesAreUppercasedAndQuoted(t *testing.T) {
	p, err := ParamsFromProperties(props(
		"STEP 3-", "1 RED HEART",
		"STEP 4-First Name-", "vasu",
		"STEP 5-Second Name", "Padmanabh",
	))
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if p.NameLeft != "VASU" || p.NameRight != "PADMANABH" {
		t.Errorf("names = %q/%q, want VASU/PADMANABH", p.NameLeft, p.NameRight)
	}

	args := p.Args()
	if args["NAME_L"] != `"VASU"` {
		t.Errorf("NAME_L = %s, want a quoted OpenSCAD literal", args["NAME_L"])
	}
	// The finished size is asserted in TestArgsPinTheFinishedSize.
}

// A name is customer-supplied text going onto a command line as an OpenSCAD
// literal. Quote is what stops a stray quote mark becoming syntax.
func TestQuoteEscapes(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`VASU`, `"VASU"`},
		{`VA"SU`, `"VA\"SU"`},
		{`VA\SU`, `"VA\\SU"`},
	} {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// The finished size must be on every render. Without it the template grows the
// plate to fit the text and the model comes out at its natural size - about
// 317mm for a seven-letter pair.
func TestArgsPinTheFinishedSize(t *testing.T) {
	p, err := ParamsFromProperties(liveOrder("1 RED HEART"))
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	args := p.Args()
	for k, want := range map[string]string{
		"OUT_X": "200",
		"OUT_Y": "50",
		"OUT_Z": "40",
	} {
		if args[k] != want {
			t.Errorf("%s = %q, want %q", k, args[k], want)
		}
	}

	// Margins belong to the templates, which the shop edits directly. Setting
	// them here would make a change to the .scad quietly do nothing.
	for _, k := range []string{"MARGIN_L", "MARGIN_R"} {
		if _, ok := args[k]; ok {
			t.Errorf("%s must come from the template, not from Go", k)
		}
	}
}

// A long name on the two-heart plank needs wider margins: two hearts already
// consume padding slots, so a long name on top of them runs the glyphs into
// the plate edge.
func TestWideMarginsForLongTwoHeartNames(t *testing.T) {
	for _, c := range []struct {
		name         string
		hearts       string
		left, right  string
		wantOverride bool
	}{
		// Seven is the threshold itself, not past it - the file's 60/60 stands.
		{"exactly seven letters", "2 RED HEART", "SUBHANJ", "SUBHANT", false},
		{"short names", "2 RED HEART", "AMY", "BOB", false},
		// Either side counts: the two are one intersected run, so the longer
		// name sets the width whichever side it came from.
		{"long first name", "2 RED HEART", "SUBHANJANA", "BOB", true},
		{"long second name", "2 RED HEART", "AMY", "SUBHANTIKA", true},
		{"both long", "2 RED HEART", "SUBHANJANA", "SUBHANTIKA", true},
		// Only the two-heart template. The others have their own margins for
		// their own reasons - 12/50 makes room for the single heart.
		{"one heart, long name", "1 RED HEART", "SUBHANJANA", "SUBHANTIKA", false},
		{"no heart, long name", "NO HEART", "SUBHANJANA", "SUBHANTIKA", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParamsFromProperties(props(
				"STEP 3-", c.hearts,
				"STEP 4-First Name-", c.left,
				"STEP 5-Second Name", c.right,
			))
			if err != nil {
				t.Fatalf("params: %v", err)
			}
			args := p.Args()
			left, hasLeft := args["MARGIN_L"]
			right, hasRight := args["MARGIN_R"]

			if !c.wantOverride {
				if hasLeft || hasRight {
					t.Errorf("margins were overridden (%s/%s); the template's own should stand",
						left, right)
				}
				return
			}
			if left != "70" || right != "70" {
				t.Errorf("margins = %s/%s (set=%v/%v), want 70/70", left, right, hasLeft, hasRight)
			}
		})
	}
}

// The storefront renames these fields, and Tensor had no idea.
//
// Nine live orders carried "First Name on Plank" / "Second Name on Plank" and
// every one failed to render with "a plank needs both names" - the names were
// in the order all along, under a label one word longer than the list the
// matcher compared against.
func TestParamsFromPropertiesReadsTheStorefrontsLongerLabels(t *testing.T) {
	// Verbatim from order T3DPS-114608.
	props := []production.LineProp{
		{Name: "_has_gpo", Value: "1021651"},
		{Name: "Red Heart", Value: "2 Red Heart"},
		{Name: "First Name on Plank", Value: "ALLEN"},
		{Name: "Second Name on Plank", Value: "ANGEL"},
		{Name: "Whatsapp Number to send DEMO Video", Value: "9789273973"},
		{Name: "_gpo_addon_price", Value: "50"},
	}

	p, err := ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if p.NameLeft != "ALLEN" || p.NameRight != "ANGEL" {
		t.Errorf("names = %q / %q, want ALLEN / ANGEL", p.NameLeft, p.NameRight)
	}
	if p.Hearts != 2 {
		t.Errorf("hearts = %d, want 2 (from %q)", p.Hearts, "2 Red Heart")
	}
}

// An exact label still wins over a longer one that merely contains it, so a
// store carrying both resolves to the field it meant.
func TestParamsFromPropertiesPrefersTheExactLabel(t *testing.T) {
	props := []production.LineProp{
		{Name: "First Name on Plank", Value: "WRONG"},
		{Name: "First Name", Value: "RIGHT"},
		{Name: "Second Name", Value: "OTHER"},
		{Name: "Hearts", Value: "1"},
	}
	p, err := ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if p.NameLeft != "RIGHT" {
		t.Errorf("first name = %q, want the exact label's value", p.NameLeft)
	}
}

// Whole words only. A label that happens to contain the letters of a key is not
// that key - getting a heart count out of "Heartfelt Message" would print the
// wrong plank.
func TestContainsPhraseMatchesWholeWordsOnly(t *testing.T) {
	for _, tc := range []struct {
		label, key string
		want       bool
	}{
		{"first name on plank", "first name", true},
		{"red heart", "heart", true},
		{"step 4 first name", "first name", true},
		{"heartfelt message", "heart", false},
		{"name of first pet", "first name", false},
		{"second name on plank", "first name", false},
		{"", "heart", false},
		{"heart", "", false},
	} {
		if got := containsPhrase(tc.label, tc.key); got != tc.want {
			t.Errorf("containsPhrase(%q, %q) = %v, want %v", tc.label, tc.key, got, tc.want)
		}
	}
}

// A combo product's options are not a plank's options.
//
// Verbatim from order T3DPS-114753, an order for a 3D rose and a heart
// keychain: its "STEP 3" is the SECOND NAME, and it carries a field called
// "Name On Heart Keychain" that mentions hearts and holds a name. Reading a
// heart count out of either produced "could not read a heart count from
// \"PAVI\"" - a plank rendered from that would be wrong in a way nobody would
// catch until it came off the printer.
func TestParamsFromPropertiesRefusesACombosOptionsAsHearts(t *testing.T) {
	props := []production.LineProp{
		{Name: "_has_gpo", Value: "1255456"},
		{Name: "STEP 2 - First Name-", Value: "AATHI"},
		{Name: "STEP 3 -Second Name", Value: "PAVI"},
		{Name: "STEP 4 - Name On 3D Rose", Value: "PAVITHRA"},
		{Name: "STEP 5 - Name On Heart Keychain", Value: "AATHI PAVI"},
		{Name: "STEP 6 - WhatsApp Number", Value: "7010078156"},
	}

	_, err := ParamsFromProperties(props)
	if err == nil {
		t.Fatal("a combo order was accepted as a plank; it has no heart count at all")
	}
	if !strings.Contains(err.Error(), "how many hearts") {
		t.Errorf("error = %v, want it to say the order does not give a heart count", err)
	}

	// The names must still resolve - the step numbers moved, not the meaning.
	if got := lookup(props, keyFirstName...); got != "AATHI" {
		t.Errorf("first name = %q, want AATHI from a shifted step number", got)
	}
	if got := lookup(props, keySecondName...); got != "PAVI" {
		t.Errorf("second name = %q, want PAVI", got)
	}
}

// The heart count is taken from the first label whose VALUE reads as one, not
// the first label that mentions hearts.
func TestHeartsFromPropertiesSkipsALabelHoldingAName(t *testing.T) {
	props := []production.LineProp{
		{Name: "Name On Heart Keychain", Value: "AATHI PAVI"},
		{Name: "Red Heart", Value: "2 Red Heart"},
	}
	hearts, ok := heartsFromProperties(props)
	if !ok || hearts != 2 {
		t.Errorf("hearts = %d (found %v), want 2 - the keychain's name is not a count", hearts, ok)
	}
}
