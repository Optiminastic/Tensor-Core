package personalise

import (
	"reflect"
	"testing"
)

func TestATemplateDeclaresItsOwnColours(t *testing.T) {
	// The lotus holder's own header, as written.
	source := []byte(`
/* [Colours] */
// Any colour name ("gold", "purple") or hex ("#A235D8"). Defaults = the 3MF.
BACK_COLOR      = "#A235D8";  // background lotus (purple)
OUTLINE_COLOR   = "#FF7FFF";  // lotus border lines
TEXT_COLOR      = "#FFBF00";  // name / text (gold)
DIYA_COLOR      = "#FF7FFF";  // diya (tealight) cup
TEXT            = "DIWALI";
BOX_W           = 100;
`)
	got := PartColours(source)
	want := map[string]string{
		"back":    "#A235D8",
		"outline": "#FF7FFF",
		"text":    "#FFBF00",
		"diya":    "#FF7FFF",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("part colours = %#v, want %#v", got, want)
	}
}

func TestAColourItCannotReadIsLeftToTheRegistry(t *testing.T) {
	// Each of these is a real way an author writes a colour, and none of them
	// is a hex this can paint with. Left OUT rather than guessed at: the
	// caller falls back to the registry, which is a colour somebody chose.
	source := []byte(`
BACK_COLOR    = "gold";          // a name OpenSCAD understands, not a hex
OUTLINE_COLOR = [1, 0.5, 0];     // a vector
TEXT_COLOR    = "#FFF";          // short form
DIYA_COLOR    = "#GGHHII";       // not hex at all
PLATE_COLOR   = "#1b7f3b";       // lower case IS fine
`)
	got := PartColours(source)
	want := map[string]string{"plate": "#1B7F3B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("part colours = %#v, want %#v - only a readable hex may be used", got, want)
	}
}

func TestAFileWithNoColoursSaysSo(t *testing.T) {
	// The plank: no *_COLOR variables at all. An empty map, not nil-with-
	// surprises, so the caller's lookup simply misses and the registry
	// answers - which is how every product behaved before this existed.
	got := PartColours([]byte(`NAME_L = "ASHA"; OUT_X = 200; PART = "base";`))
	if len(got) != 0 {
		t.Fatalf("part colours = %#v, want none", got)
	}
}
