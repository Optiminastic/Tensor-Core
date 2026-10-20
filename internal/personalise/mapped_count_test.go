package personalise

// Mapping a heart count from the registry.
//
// The registry could describe a field as a string or as a number, and the
// storefront's heart option is neither: it sells "1 RED HEART", renames it to
// "2 PINK HEARTS" when the catalogue changes, and says "NO HEART" for none.
// Mapped as a string it reaches OpenSCAD as side_hearts="1 RED HEART", which
// is not a number and fails every comparison the template makes against it;
// mapped as a number it is refused and the job is held. Either way the plank -
// the highest-volume product in the shop - could not be described in the
// registry at all, which is why every change to it needed a deploy.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// The wording, as the storefront actually writes it.
func TestCountReadsTheStorefrontsWording(t *testing.T) {
	for _, c := range []struct {
		value string
		want  string
	}{
		{"1 RED HEART", "1"},
		{"2 PINK HEARTS", "2"},
		{"2", "2"},
		// Says none, so it means none. Only wording that SAYS so reads as
		// zero - anything else is refused below.
		{"NO HEART", "0"},
		{"none", "0"},
		{"Without heart", "0"},
	} {
		t.Run(c.value, func(t *testing.T) {
			args, err := MappedArgs(
				lineProps("STEP 3-", c.value),
				[]FieldMap{{PropertyKey: "step 3", ScadVariable: "side_hearts", Kind: ValueCount}},
			)
			if err != nil {
				t.Fatalf("mapped args: %v", err)
			}
			// UNQUOTED, like a number. side_hearts is compared
			// arithmetically in the template, and a quoted "1" fails every
			// one of those comparisons without erroring.
			if args["side_hearts"] != c.want {
				t.Errorf("side_hearts = %q, want %q", args["side_hearts"], c.want)
			}
		})
	}
}

// Wording that names no number and does not say none is REFUSED.
//
// Not guessed at. A count invented from an option nobody anticipated renders
// the wrong product and exits 0, which is the failure this whole package is
// arranged against.
func TestCountRefusesWordingItCannotRead(t *testing.T) {
	_, err := MappedArgs(
		lineProps("STEP 3-", "KRISHNA"),
		[]FieldMap{{PropertyKey: "step 3", ScadVariable: "side_hearts", Kind: ValueCount}},
	)
	if err == nil {
		t.Fatal("a name was accepted as a heart count")
	}
	// The message has to name the variable and quote what the order said, or
	// the person reading the issues board cannot tell which field is wrong.
	for _, want := range []string{"side_hearts", "KRISHNA"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An unanswered optional field takes its DEFAULT.
//
// This is the case that makes the plank describable. The 2-STEP plank sells no
// heart option at all, so nothing on those lines names one: the Go path reads
// that as zero. Required, those orders would be held for a choice the customer
// was never offered; optional with no default, the joinery template's own
// side_hearts = 1 would stand and every one of them would grow a heart nobody
// bought.
func TestUnansweredOptionalFieldTakesItsDefault(t *testing.T) {
	args, err := MappedArgs(
		// A 2-STEP plank: two names and NO heart property at all.
		lineProps("STEP 4-First Name-", "KALYANI", "STEP 5-Second Name", "KRISHNA"),
		[]FieldMap{
			{PropertyKey: "step 4 first name", ScadVariable: "name1"},
			{PropertyKey: "step 5 second name", ScadVariable: "name2"},
			{PropertyKey: "step 3", ScadVariable: "side_hearts",
				Kind: ValueCount, Optional: true, Default: "0"},
		},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if args["side_hearts"] != "0" {
		t.Errorf("side_hearts = %q, want \"0\" - this plank sells no heart option",
			args["side_hearts"])
	}
}

// Without a default, an unanswered optional field is still not passed at all.
//
// The old behaviour, unchanged: a rose the customer did not name should take
// the template's own default, and `-D TEXT=""` engraves nothing where the
// .scad might have left the surface plain. Those are different objects.
func TestUnansweredOptionalFieldWithoutADefaultPassesNothing(t *testing.T) {
	args, err := MappedArgs(
		lineProps("STEP 4-First Name-", "KALYANI"),
		[]FieldMap{
			{PropertyKey: "step 4 first name", ScadVariable: "name1"},
			{PropertyKey: "rose name", ScadVariable: "ROSE_TEXT", Optional: true},
		},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if _, passed := args["ROSE_TEXT"]; passed {
		t.Errorf("ROSE_TEXT was passed as %q; an unanswered field with no default "+
			"must leave the template's own default standing", args["ROSE_TEXT"])
	}
}

// A default is typed exactly as a mapped value is.
//
// Shared through writeValue so the two cannot drift: "no heart" in the default
// box has to mean zero exactly as a customer's "NO HEART" does, and a default
// that cannot be read has to be refused exactly as a customer's would be.
func TestDefaultIsTypedLikeAnAnswer(t *testing.T) {
	args, err := MappedArgs(nil, []FieldMap{
		{PropertyKey: "step 3", ScadVariable: "side_hearts",
			Kind: ValueCount, Optional: true, Default: "NO HEART"},
	})
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if args["side_hearts"] != "0" {
		t.Errorf("side_hearts = %q, want \"0\"", args["side_hearts"])
	}

	_, err = MappedArgs(nil, []FieldMap{
		{PropertyKey: "step 3", ScadVariable: "side_hearts",
			Kind: ValueCount, Optional: true, Default: "lots"},
	})
	if err == nil {
		t.Fatal("a default of \"lots\" was accepted for a count")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error %q does not say the DEFAULT is the broken one, so the "+
			"reader would go looking at the order", err)
	}
}

// THE TWO PATHS MUST AGREE.
//
// The plank renders from Go today and from the registry once its mapping is
// seeded. The whole point of the registry is that the switch changes WHERE the
// answer is written down, not WHAT it is - so the same order has to produce
// the same -D flags either way. If this drifts, moving a product onto the
// registry silently changes the geometry it has been printing.
func TestTheRegistryMappingAgreesWithTheGoPath(t *testing.T) {
	// The mapping cmd/seedregistry writes, stated here so a change to it that
	// nobody checked against the Go path fails.
	plankMapping := []FieldMap{
		{PropertyKey: "step 4 first name", ScadVariable: "name1"},
		{PropertyKey: "step 5 second name", ScadVariable: "name2"},
		{PropertyKey: "step 3", ScadVariable: "side_hearts",
			Kind: ValueCount, Optional: true, Default: "0"},
		{ScadVariable: "plank_length", Kind: ValueNumber, Fixed: "200"},
		{ScadVariable: "plank_width", Kind: ValueNumber, Fixed: "50"},
		{ScadVariable: "total_height", Kind: ValueNumber, Fixed: "40"},
	}

	for _, c := range []struct {
		name  string
		props []production.LineProp
	}{
		{"two hearts", lineProps(
			"STEP 3-", "2 RED HEART",
			"STEP 4-First Name-", "AMENA",
			"STEP 5-Second Name", "SALMAN")},
		{"one heart", lineProps(
			"STEP 3-", "1 RED HEART",
			"STEP 4-First Name-", "PULKIT",
			"STEP 5-Second Name", "SONAL")},
		{"no heart", lineProps(
			"STEP 3-", "NO HEART",
			"STEP 4-First Name-", "JANATH",
			"STEP 5-Second Name", "KHAJA")},
		// The shape with no heart property at all - the one the default
		// exists for.
		{"heart option not offered", lineProps(
			"STEP 4-First Name-", "KALYANI",
			"STEP 5-Second Name", "KRISHNA")},
	} {
		t.Run(c.name, func(t *testing.T) {
			fromGo, err := ParamsFromProperties(c.props)
			if err != nil {
				t.Fatalf("the Go path: %v", err)
			}
			goArgs := fromGo.Args()

			registryArgs, err := MappedArgs(c.props, plankMapping)
			if err != nil {
				t.Fatalf("the registry path: %v", err)
			}

			if len(goArgs) != len(registryArgs) {
				t.Errorf("the two paths pass different numbers of flags: Go %v, registry %v",
					goArgs, registryArgs)
			}
			for name, want := range goArgs {
				if got := registryArgs[name]; got != want {
					t.Errorf("%s = %q from the registry, %q from Go - the same order "+
						"would render differently on either side of the switch",
						name, got, want)
				}
			}
		})
	}
}
