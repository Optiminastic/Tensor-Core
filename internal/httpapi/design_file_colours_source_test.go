package httpapi

import (
	"os"
	"testing"
)

// The API on prod has no OpenSCAD, so a file's colours are judged from its
// source there. A file whose colours come from an order variable passes; a
// plank whose colours are fixed strings, and a file colouring only one piece,
// do not.
func TestColoursFollowTheOrderFromSource(t *testing.T) {
	desk := `colors = "Gold & Black";
COL_PARTS = split_amp(colors);
LETTER_COLOR = to_hex(COL_PARTS[0]);
STAND_COLOR  = to_hex(COL_PARTS[1]);
NAME_COLOR = LETTER_COLOR;  // letters
BASE_COLOR = STAND_COLOR;
module out_base() { color(BASE_COLOR) base(); }
module out_name() { color(NAME_COLOR) name(); }
`
	if !coloursFollowTheOrder([]byte(desk)) {
		t.Error("a file colouring two pieces from the order's colours was refused")
	}

	plank, err := os.ReadFile("../personalise/templates/dnp_two_heart.scad")
	if err != nil {
		t.Fatal(err)
	}
	if coloursFollowTheOrder(plank) {
		t.Error("the plank colours from fixed strings (TEXT_COLOR = \"YELLOW\") and must be refused")
	}

	one := "c = pick(x);\ncolor(c) a();\n// color(OTHER) b();\n"
	if coloursFollowTheOrder([]byte(one)) {
		t.Error("one coloured piece (the second is commented out) must be refused")
	}
}
