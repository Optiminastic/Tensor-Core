package personalise

// A preview file that drew four stacked copies of the whole product.
//
// The Royal holder's assembled file declares part and text; Tensor passes
// PART and TEXT. OpenSCAD takes a -D for a variable it does not have and
// never reads it, so part kept its default "all" and every piece pass drew
// the entire model - and the customer's name never arrived at all. Neither
// failure said anything: the render exited 0 and the picture looked like a
// product.

import (
	"strings"
	"testing"
)

const lowerCaseFile = `
text = "DIWALI AT SWAMY'S";
part = "all";
background_color = "purple";
`

const upperCaseFile = `
TEXT = "DIWALI AT SWAMY'S";
PART = "all";
PLATE_COLOR = "purple";
`

func TestArgumentsTakeTheSpellingTheFileDeclares(t *testing.T) {
	got := AlignToSource([]byte(lowerCaseFile), map[string]string{
		"PART": `"plate"`, "TEXT": `"Asha"`,
	})

	if v, ok := got["part"]; !ok || v != `"plate"` {
		t.Errorf("part = %q (present=%v); the file calls it part, so that is what "+
			"-D must say or every piece draws the whole model", v, ok)
	}
	if v, ok := got["text"]; !ok || v != `"Asha"` {
		t.Errorf("text = %q (present=%v); the customer's name never reached the file", v, ok)
	}
	if _, ok := got["PART"]; ok {
		t.Error("PART is still passed alongside part; the file would read neither reliably")
	}
	if _, ok := got["TEXT"]; ok {
		t.Error("TEXT is still passed alongside text")
	}
}

func TestAFileThatAlreadyAgreesIsUntouched(t *testing.T) {
	// The plank range and every embedded template declare upper case, and
	// this runs on their path too. A no-op here is what makes that safe.
	in := map[string]string{"PART": `"plate"`, "TEXT": `"Asha"`}
	got := AlignToSource([]byte(upperCaseFile), in)

	if len(got) != len(in) {
		t.Fatalf("got %d arguments, want %d", len(got), len(in))
	}
	for name, want := range in {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
}

func TestAnUndeclaredArgumentIsPassedAsWritten(t *testing.T) {
	// It does nothing either way. Dropping it would hide a mapping mistake
	// from the registry checks whose job is to report exactly that.
	got := AlignToSource([]byte(upperCaseFile), map[string]string{"NOT_IN_FILE": `"x"`})
	if got["NOT_IN_FILE"] != `"x"` {
		t.Errorf("NOT_IN_FILE = %q, want it passed through unchanged", got["NOT_IN_FILE"])
	}
}

func TestAlignmentAgreesWithTheCheckThatLetTheMappingThrough(t *testing.T) {
	// THE TWO HALVES OF THE SAME RULE. DeclaresAll lower-cases both sides, so
	// a mapping written TEXT against a file declaring text passes validation.
	// Having said that is fine, the render has to make it true.
	if missing := DeclaresAll([]byte(lowerCaseFile), []string{"TEXT", "PART"}); len(missing) != 0 {
		t.Fatalf("DeclaresAll reports %v missing; this test's premise is gone", missing)
	}
	got := AlignToSource([]byte(lowerCaseFile), map[string]string{"TEXT": `"Asha"`})
	for name := range got {
		if name != strings.TrimSpace(name) || name == "TEXT" {
			t.Errorf("validation accepted TEXT but the render still passes %q", name)
		}
	}
}

func TestTheFirstDeclarationWinsWhenAFileSpellsItBothWays(t *testing.T) {
	// Pathological, and the point is only that two renders of it agree.
	src := []byte("PART = \"all\";\npart = \"all\";\n")
	got := AlignToSource(src, map[string]string{"part": `"plate"`})
	if _, ok := got["PART"]; !ok {
		t.Errorf("got %v, want the first declaration's spelling", got)
	}
}
