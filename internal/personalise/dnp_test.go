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

// A line offering no heart option renders with no hearts.
//
// This test used to assert the opposite, on the grounds that "the customer
// chose none" and "the property did not import" were indistinguishable. They
// are distinguishable, by looking at the LABELS rather than only at the values:
// a label that names hearts says the option was offered, and that case still
// errors - TestParamsFromPropertiesStopsWhenAnOfferedHeartOptionWillNotRead.
// Nothing naming hearts means the product does not sell them.
//
// The case the old test was really protecting against - an order that imported
// with no properties at all - never reaches here: it fails the two-names check
// above, and GenerateModelForJob stops it earlier still with "this order
// carries no personalisation options".
func TestNoHeartOptionOfferedRendersWithNoHearts(t *testing.T) {
	p := props("STEP 4-First Name-", "VASU", "STEP 5-Second Name", "PADMANABH")
	got, err := ParamsFromProperties(p)
	if err != nil {
		t.Fatalf("ParamsFromProperties = %v, want a no-heart plank", err)
	}
	if got.Hearts != 0 || got.Template != templateNoHeart {
		t.Errorf("hearts = %d, template = %q; want 0 and %q",
			got.Hearts, got.Template, templateNoHeart)
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

// Margins come from the .scad and from nowhere else.
//
// Go used to widen them to 70/70 for a long name on the two-heart plank, on
// the reasoning that two hearts already consume padding slots. That override
// now CONTRADICTS the templates it was helping: the two-heart file specifies
// 75/75, so the override would have quietly narrowed the very case it existed
// to give room to, and for most names - anything over seven letters.
//
// The shop edits these files directly, so a value in the .scad that Go
// silently replaces is the worst of both: the file says one thing and the
// plank is another.
func TestMarginsAlwaysComeFromTheTemplate(t *testing.T) {
	for _, c := range []struct {
		name   string
		hearts string
		left   string
		right  string
	}{
		{"two hearts, short names", "2 RED HEART", "AMY", "BOB"},
		{"two hearts, exactly seven", "2 RED HEART", "SUBHANJ", "SUBHANT"},
		// The cases Go used to override. They must now take the file's 75/75.
		{"two hearts, long first name", "2 RED HEART", "SUBHANJANA", "BOB"},
		{"two hearts, long second name", "2 RED HEART", "AMY", "SUBHANTIKA"},
		{"two hearts, both long", "2 RED HEART", "SUBHANJANA", "SUBHANTIKA"},
		{"one heart, long name", "1 RED HEART", "SUBHANJANA", "SUBHANTIKA"},
		{"no heart, long name", "NO HEART", "SUBHANJANA", "SUBHANTIKA"},
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
			for _, k := range []string{"MARGIN_L", "MARGIN_R"} {
				if v, ok := args[k]; ok {
					t.Errorf("%s = %q; it must come from the template, not from Go", k, v)
				}
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

// A combo product's OTHER items are not the plank's heart count.
//
// Verbatim from order T3DPS-114753. This test used to assert the combo was
// refused outright, on the reading that it "is a different product". The live
// data says otherwise: the Soulmate COMBO carries a real plank SKU
// (T3DPS-DNP-2 / -3) and sells a plank alongside a 3D rose and a heart
// keychain. Its plank is an ordinary no-heart plank whose names sit on STEP 2
// and STEP 3.
//
// Refusing it also produced an inconsistency that gave the game away: three
// live orders of the same COMBO family, of which the one WITHOUT a keychain
// field rendered and the two WITH one refused - the difference being a label
// about a keychain, not anything about the plank.
//
// So both traps still have to be avoided, but by exclusion rather than
// refusal: "STEP 3 -Second Name" is a name, and "Name On Heart Keychain" is
// another item's engraving. Neither is a heart count; with both out of the
// way the line offers no heart option at all, which is zero.
func TestParamsFromPropertiesReadsACombosPlankWithNoHearts(t *testing.T) {
	props := []production.LineProp{
		{Name: "_has_gpo", Value: "1255456"},
		{Name: "STEP 2 - First Name-", Value: "AATHI"},
		{Name: "STEP 3 -Second Name", Value: "PAVI"},
		{Name: "STEP 4 - Name On 3D Rose", Value: "PAVITHRA"},
		{Name: "STEP 5 - Name On Heart Keychain", Value: "AATHI PAVI"},
		{Name: "STEP 6 - WhatsApp Number", Value: "7010078156"},
	}

	got, err := ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("ParamsFromProperties = %v, want the combo's plank", err)
	}
	if got.Hearts != 0 || got.Template != templateNoHeart {
		t.Errorf("hearts = %d, template = %q; want 0 and %q - neither the second name "+
			"nor the keychain's engraving is a heart count",
			got.Hearts, got.Template, templateNoHeart)
	}

	// The names must still resolve - the step numbers moved, not the meaning.
	if got.NameLeft != "AATHI" || got.NameRight != "PAVI" {
		t.Errorf("names = %q/%q, want AATHI/PAVI from shifted step numbers",
			got.NameLeft, got.NameRight)
	}
}

// The heart count is taken from the first label whose VALUE reads as one, not
// the first label that mentions hearts.
func TestHeartsFromPropertiesSkipsALabelHoldingAName(t *testing.T) {
	props := []production.LineProp{
		{Name: "Name On Heart Keychain", Value: "AATHI PAVI"},
		{Name: "Red Heart", Value: "2 Red Heart"},
	}
	hearts, found := heartsFromProperties(props)
	if found != heartsFound || hearts != 2 {
		t.Errorf("hearts = %d (%v), want 2 - the keychain's name is not a count", hearts, found)
	}
}

// A product that does not sell hearts renders with none, rather than not at all.
//
// Verbatim from JOB-114931. This shape numbers its steps differently - STEP 2
// is the first name and STEP 3 is the SECOND NAME - and carries no heart
// property, because the product has no heart option. Two faults met here:
// "step 3" was listed as a heart key, so the lookup read the customer's second
// name "KRISHNA" as a heart count; and finding nothing readable was then
// treated as an error. Four live orders sat unrendered behind a message about
// a choice the customer was never offered.
func TestParamsFromPropertiesDefaultsToNoHeartsWhenNoneAreOffered(t *testing.T) {
	props := []production.LineProp{
		{Name: "_has_gpo", Value: "1255456"},
		{Name: "STEP 2 - First Name-", Value: "KALYANI"},
		{Name: "STEP 3 -Second Name", Value: "KRISHNA"},
	}

	got, err := ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("ParamsFromProperties = %v, want a no-heart plank", err)
	}
	if got.Hearts != 0 {
		t.Errorf("hearts = %d, want 0", got.Hearts)
	}
	if got.NameLeft != "KALYANI" || got.NameRight != "KRISHNA" {
		t.Errorf("names = %q/%q, want KALYANI/KRISHNA - the step numbers moved, not the meaning",
			got.NameLeft, got.NameRight)
	}
	if got.Template != templateNoHeart {
		t.Errorf("template = %q, want %q", got.Template, templateNoHeart)
	}
}

// The main plank shape, which must keep working unchanged: "STEP 3-" holds the
// heart count and is matched positionally, because it names nothing.
func TestParamsFromPropertiesStillReadsThePositionalHeartOption(t *testing.T) {
	for value, want := range map[string]int{"2 RED HEART": 2, "1 RED HEART": 1, "0 RED HEART": 0} {
		props := []production.LineProp{
			{Name: "STEP 4-First Name-", Value: "VASU"},
			{Name: "STEP 5-Second Name", Value: "PADMA"},
			{Name: "STEP 3-", Value: value},
		}
		got, err := ParamsFromProperties(props)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if got.Hearts != want {
			t.Errorf("%q gave %d hearts, want %d", value, got.Hearts, want)
		}
	}
}

// A heart option that IS offered but will not read still stops.
//
// This is the half of the old refusal worth keeping. "The product has no heart
// option" and "the heart option failed to import" look identical only if you
// refuse to look at the label; a label naming hearts says the option existed,
// so an unreadable value there is a fault rather than a choice. Printing it as
// zero would lay the names out with 12mm margins where 60mm were wanted.
func TestParamsFromPropertiesStopsWhenAnOfferedHeartOptionWillNotRead(t *testing.T) {
	props := []production.LineProp{
		{Name: "STEP 4-First Name-", Value: "VASU"},
		{Name: "STEP 5-Second Name", Value: "PADMA"},
		{Name: "Red Heart", Value: "please surprise me"},
	}
	if _, err := ParamsFromProperties(props); err == nil {
		t.Fatal("an unreadable heart option was defaulted to zero; it must stop")
	}
}

// A label that names a name is a name, whatever step number it carries.
func TestIsNameLabel(t *testing.T) {
	for label, want := range map[string]bool{
		"step 3 second name":     true,
		"step 2 first name":      true,
		"step 5 second name":     true,
		"first name on plank":    true,
		"step 3":                 false,
		"red heart":              false,
		"step 6 whatsapp number": false,
	} {
		if got := isNameLabel(label); got != want {
			t.Errorf("isNameLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

// The Dual Name & Photo Frame is the same two names as the plank, scaled into a
// frame instead. It prints from the SAME no-heart template, so the only thing
// separating the two products is the finished size - which makes getting that
// size right the whole job.
func TestPhotoFrameRendersAtItsOwnSize(t *testing.T) {
	base := Params{Template: templateTwoHeart, NameLeft: "ASHA", NameRight: "RAVI", Hearts: 2}

	for _, c := range []struct{ name, sku, product string }{
		{"by SKU segment", "T3DPS-DNPF-2", ""},
		{"by product name", "", "DUAL NAME & PHOTO FRAME"},
		{"by lower-cased name", "", "dual name & photo frame"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := base.ForProduct(c.sku, c.product)
			if got.Shape != FrameShape {
				t.Errorf("shape = %+v, want %+v", got.Shape, FrameShape)
			}
			// Its own key, not the plank's. Forced rather than inherited too:
			// a frame has no hearts to offer, and a stray heart property must
			// not pick a template whose margins are laid out for a plank.
			if got.Template != templateFrame {
				t.Errorf("template = %q, want %q", got.Template, templateFrame)
			}
			if got.Hearts != 0 {
				t.Errorf("hearts = %d, want 0", got.Hearts)
			}
			args := got.Args()
			for name, want := range map[string]string{"OUT_X": "150", "OUT_Y": "40", "OUT_Z": "40"} {
				if args[name] != want {
					t.Errorf("%s = %q, want %q", name, args[name], want)
				}
			}
		})
	}
}

// The prefix mistake, guarded. "DNP" and "DNPLB" both start the same way as
// "DNPF", and treating the frame as a plank - or a plank as a frame - prints
// the wrong object at the wrong size.
func TestPlankIsNotMistakenForAFrame(t *testing.T) {
	base := Params{Template: templateTwoHeart, NameLeft: "ASHA", NameRight: "RAVI", Hearts: 2}

	for _, c := range []struct{ name, sku, product string }{
		{"plain plank SKU", "T3DPS-DNP-2", "Dual Name Plank"},
		{"with-light plank SKU", "DNPLB-RED", "Dual Name Plank"},
		{"a name that merely contains dnpf", "", "DNPFRAMEWORK"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := base.ForProduct(c.sku, c.product)
			if got.Shape != (Shape{}) && got.Shape != PlankShape {
				t.Errorf("shape = %+v, want the plank's", got.Shape)
			}
			if got.Template != templateTwoHeart {
				t.Errorf("template = %q, want it left alone", got.Template)
			}
			args := got.Args()
			if args["OUT_X"] != "200" || args["OUT_Y"] != "50" {
				t.Errorf("OUT_X/OUT_Y = %q/%q, want 200/50", args["OUT_X"], args["OUT_Y"])
			}
		})
	}
}

// The photo frame asks for the same two names as the plank under its own
// labels. Real order T3DPS-114836 carries exactly this shape.
//
// Without these labels every frame order failed with "a plank needs both
// names", which reads like the customer left the fields blank when they had
// filled both in - and the photo properties sitting beside them must not be
// mistaken for names.
func TestFrameNamePropertiesAreRead(t *testing.T) {
	props := []production.LineProp{
		{Name: "_has_gpo", Value: "854058"},
		{Name: "LEFT NAME", Value: "AMENA"},
		{Name: "LEFT PHOTO", Value: "https://example.test/left.jpg"},
		{Name: "RIGHT NAME", Value: "SALMAN"},
		{Name: "RIGHT PHOTO", Value: "https://example.test/right.jpg"},
	}

	got, err := ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if got.NameLeft != "AMENA" || got.NameRight != "SALMAN" {
		t.Errorf("names = %q/%q, want AMENA/SALMAN", got.NameLeft, got.NameRight)
	}

	frame := got.ForProduct("DNPF-1", "DUAL NAME & PHOTO FRAME - GOLD")
	args := frame.Args()
	if args["NAME_L"] != `"AMENA"` || args["NAME_R"] != `"SALMAN"` {
		t.Errorf("NAME_L/NAME_R = %s/%s", args["NAME_L"], args["NAME_R"])
	}
	if args["OUT_X"] != "150" || args["OUT_Y"] != "40" || args["OUT_Z"] != "40" {
		t.Errorf("size = %s/%s/%s, want 150/40/40", args["OUT_X"], args["OUT_Y"], args["OUT_Z"])
	}
}
