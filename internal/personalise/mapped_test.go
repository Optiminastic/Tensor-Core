package personalise

import (
	"errors"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func lineProps(pairs ...string) []production.LineProp {
	out := make([]production.LineProp, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, production.LineProp{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func TestMappedArgsBuildsTheFlags(t *testing.T) {
	args, err := MappedArgs(
		lineProps(
			"STEP 4-First Name-", "AMENA",
			"STEP 5-Second Name", "SALMAN",
			"STEP 3-", "2",
		),
		[]FieldMap{
			{PropertyKey: "step 4 first name", ScadVariable: "NAME_L"},
			{PropertyKey: "step 5 second name", ScadVariable: "NAME_R"},
			{PropertyKey: "step 3", ScadVariable: "HEARTS", Numeric: true},
		},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	// Strings quoted, numbers not. An unquoted string is a syntax error; a
	// quoted number fails every comparison the script makes without erroring.
	for k, want := range map[string]string{
		"NAME_L": `"AMENA"`, "NAME_R": `"SALMAN"`, "HEARTS": "2",
	} {
		if args[k] != want {
			t.Errorf("%s = %s, want %s", k, args[k], want)
		}
	}
	// PART changes per render - base and lettering are two passes over the
	// same arguments - so the caller owns it.
	if _, ok := args["PART"]; ok {
		t.Error("PART was set; it belongs to the caller, which renders twice")
	}
}

func TestMappedArgsMatchesHowevertheLabelIsPunctuated(t *testing.T) {
	// The storefront writes the same field three ways across three products
	// and edits the punctuation freely.
	for _, label := range []string{
		"STEP 4-First Name-", "STEP 4 - First Name:", "step 4 first name", "STEP  4 -- First  Name",
	} {
		args, err := MappedArgs(
			lineProps(label, "AMENA"),
			[]FieldMap{{PropertyKey: "step 4 first name", ScadVariable: "NAME_L"}},
		)
		if err != nil {
			t.Errorf("%q did not match: %v", label, err)
			continue
		}
		if args["NAME_L"] != `"AMENA"` {
			t.Errorf("%q gave NAME_L = %s", label, args["NAME_L"])
		}
	}
}

func TestMappedArgsHoldsTheJobAndNamesTheField(t *testing.T) {
	for _, c := range []struct{ name, key, value string }{
		// Absent and blank are the same failure to a renderer, and blank is
		// the commoner one: the storefront sends the property with an empty
		// value when a customer skips an optional field.
		{"absent", "SOMETHING ELSE", "x"},
		{"blank", "STEP 4-First Name-", ""},
		{"whitespace only", "STEP 4-First Name-", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := MappedArgs(
				lineProps(c.key, c.value),
				[]FieldMap{{PropertyKey: "step 4 first name", ScadVariable: "NAME_L"}},
			)
			var missing *MissingFieldError
			if !errors.As(err, &missing) {
				t.Fatalf("err = %v, want a MissingFieldError", err)
			}
			// The message reaches the issues board, so it has to name the
			// field somebody must go and find.
			if missing.ScadVariable != "NAME_L" || missing.PropertyKey != "step 4 first name" {
				t.Errorf("error does not name the field: %+v", missing)
			}
		})
	}
}

func TestMappedArgsRefusesANumberThatIsNotOne(t *testing.T) {
	_, err := MappedArgs(
		lineProps("STEP 3-", "2 RED HEART"),
		[]FieldMap{{PropertyKey: "step 3", ScadVariable: "HEARTS", Numeric: true}},
	)
	if err == nil {
		// OpenSCAD would read -D HEARTS=2 RED HEART as an undefined variable,
		// render something and exit 0.
		t.Fatal("accepted a non-numeric value for a numeric field")
	}
	var missing *MissingFieldError
	if errors.As(err, &missing) {
		t.Error("reported as a missing field; the field is present and unusable")
	}
}

func TestMappedArgsIgnoresShopifyPlumbing(t *testing.T) {
	// "_has_gpo" is the options app talking to itself, not something a person
	// entered, and mapping one would map an implementation detail.
	_, err := MappedArgs(
		lineProps("_has_gpo", "622778"),
		[]FieldMap{{PropertyKey: "has gpo", ScadVariable: "NAME_L"}},
	)
	var missing *MissingFieldError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want the hidden property to be invisible", err)
	}
}

func TestMappedArgsTakesTheFirstOfARepeatedLabel(t *testing.T) {
	args, err := MappedArgs(
		lineProps("First Name", "AMENA", "First Name", "SOMEBODY ELSE"),
		[]FieldMap{{PropertyKey: "first name", ScadVariable: "NAME_L"}},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if args["NAME_L"] != `"AMENA"` {
		t.Errorf("NAME_L = %s; the first is what the customer saw at the top of the form", args["NAME_L"])
	}
}

func TestMappedArgsEscapesWhatWouldBreakTheCommandLine(t *testing.T) {
	args, err := MappedArgs(
		lineProps("Name", `AM"E\NA`),
		[]FieldMap{{PropertyKey: "name", ScadVariable: "NAME_L"}},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if args["NAME_L"] != `"AM\"E\\NA"` {
		t.Errorf("NAME_L = %s; a quote in a name must not end the literal", args["NAME_L"])
	}
}

// An optional field the order does not answer must leave the variable unset.
//
// This is the two-in-seven case: of the Soulmate Combos in the database, two
// carry no "Name On 3D Rose" at all. Under a required-everything rule those
// orders are held the day parts ship, for a box the customer chose to leave
// blank.
func TestMappedArgsLeavesAnUnansweredOptionalFieldUnset(t *testing.T) {
	for _, c := range []struct {
		name  string
		props []production.LineProp
	}{
		{"absent", lineProps("STEP 2 - First Name-", "AYUSH")},
		// The storefront sends the property with an empty value when a
		// customer skips a box, rather than omitting it. Both are "no answer".
		{"blank", lineProps(
			"STEP 2 - First Name-", "AYUSH",
			"STEP 4 - Name On 3D Rose", "   ",
		)},
	} {
		t.Run(c.name, func(t *testing.T) {
			args, err := MappedArgs(c.props, []FieldMap{
				{PropertyKey: "step 2 first name", ScadVariable: "NAME_L"},
				{PropertyKey: "step 4 name on 3d rose", ScadVariable: "TEXT", Optional: true},
			})
			if err != nil {
				t.Fatalf("an unanswered optional field must not stop the render: %v", err)
			}
			if args["NAME_L"] != `"AYUSH"` {
				t.Errorf("NAME_L = %s, want the answered field to still arrive", args["NAME_L"])
			}
			// Not set to "" - that engraves nothing where the .scad may have
			// left the surface plain, and those are different objects.
			if v, ok := args["TEXT"]; ok {
				t.Errorf("TEXT = %q; an unanswered optional field must pass no -D at all", v)
			}
		})
	}
}

func TestMappedArgsStillUsesAnOptionalFieldThatIsAnswered(t *testing.T) {
	args, err := MappedArgs(
		lineProps("STEP 4 - Name On 3D Rose", "AJ"),
		[]FieldMap{{PropertyKey: "step 4 name on 3d rose", ScadVariable: "TEXT", Optional: true}},
	)
	if err != nil {
		t.Fatalf("mapped args: %v", err)
	}
	if args["TEXT"] != `"AJ"` {
		t.Errorf("TEXT = %s, want %q", args["TEXT"], `"AJ"`)
	}
}

// Optional must not weaken the fields that hold a job. A plank with one name
// on it is scrap, and the whole point of holding is that somebody goes and
// looks.
func TestMappedArgsStillHoldsWhenARequiredFieldIsMissingBesideAnOptionalOne(t *testing.T) {
	_, err := MappedArgs(
		lineProps("STEP 4 - Name On 3D Rose", "AJ"),
		[]FieldMap{
			{PropertyKey: "step 2 first name", ScadVariable: "NAME_L"},
			{PropertyKey: "step 4 name on 3d rose", ScadVariable: "TEXT", Optional: true},
		},
	)
	var missing *MissingFieldError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want a MissingFieldError naming the required field", err)
	}
	if missing.ScadVariable != "NAME_L" {
		t.Errorf("held on %s, want NAME_L", missing.ScadVariable)
	}
}

// An optional NUMBER that is answered with something that is not a number is
// still a fault. Optional says the order need not answer, not that any answer
// will do - OpenSCAD would take `-D HEARTS=two` as an undefined reference,
// render something and exit 0.
func TestMappedArgsRefusesABadNumberEvenWhenOptional(t *testing.T) {
	_, err := MappedArgs(
		lineProps("STEP 3-", "two"),
		[]FieldMap{{PropertyKey: "step 3", ScadVariable: "HEARTS", Numeric: true, Optional: true}},
	)
	if err == nil {
		t.Fatal("an optional field answered with a non-number was accepted")
	}
}
