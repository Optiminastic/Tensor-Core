package production

import (
	"strings"
	"testing"
)

// The case this exists for, taken from the print that should never have run: a
// BLUE plate reached a machine loaded with red and white, because the check was
// left to a dispatcher that does not do it.
func TestMissingColoursCatchesTheWrongMachine(t *testing.T) {
	// P1 as the fleet sync recorded it when the blue bed printed on it.
	p1 := []LoadedFilament{
		{Colour: "#F72323", Type: "PLA"},
		{Colour: "#FFFFFF", Type: "PLA"},
	}
	need := []string{"#FFFFFF", "#1560BD"}

	missing := MissingColours(need, p1)
	if len(missing) != 1 || missing[0] != "#1560BD" {
		t.Fatalf("MissingColours = %v, want [#1560BD] - the blue this machine does not have", missing)
	}
	if HasColours(need, p1) {
		t.Error("a machine with red and white must not be allowed to print a blue plate")
	}
}

// Nearest-colour matching is deliberately NOT used, and this records why: the
// shop's own palette puts Sky Blue closer to the loaded tray than Blue is, so
// any threshold loose enough to accept Blue would also accept the wrong spool.
func TestMissingColoursDoesNotApproximate(t *testing.T) {
	// #46A8F9 is the blue actually loaded on P4; #1560BD is what the shop calls
	// BLUE. They are the same colour to a human and different to a comparison.
	loaded := []LoadedFilament{{Colour: "#FFFFFF"}, {Colour: "#46A8F9"}}
	if HasColours([]string{"#FFFFFF", "#1560BD"}, loaded) {
		t.Error("a near-enough blue was accepted; exact matching is the whole point")
	}
}

func TestMissingColours(t *testing.T) {
	white := LoadedFilament{Colour: "#FFFFFF", Type: "PLA"}
	blue := LoadedFilament{Colour: "#1560BD", Type: "PLA"}

	for _, c := range []struct {
		name   string
		need   []string
		loaded []LoadedFilament
		want   []string
	}{
		{
			name:   "both loaded",
			need:   []string{"#FFFFFF", "#1560BD"},
			loaded: []LoadedFilament{white, blue},
			want:   nil,
		},
		{
			// A tray reports 8-digit RGBA; a plate carries 6. The same colour
			// must compare equal or every bed looks unmatched.
			name:   "tray reports rgba",
			need:   []string{"#1560BD"},
			loaded: []LoadedFilament{{Colour: "1560BDFF"}},
			want:   nil,
		},
		{
			name:   "case and hash differences do not matter",
			need:   []string{"1560bd"},
			loaded: []LoadedFilament{{Colour: "#1560BD"}},
			want:   nil,
		},
		{
			// A printer whose AMS the sync has never populated is not proven to
			// hold anything. Treating empty as a wildcard would send every bed
			// to the machine Tensor knows least about - and the H2C machines,
			// which are off with empty trays, are exactly that case.
			name:   "nothing loaded is missing everything",
			need:   []string{"#FFFFFF", "#1560BD"},
			loaded: nil,
			want:   []string{"#FFFFFF", "#1560BD"},
		},
		{
			name:   "an empty tray colour matches nothing",
			need:   []string{"#FFFFFF"},
			loaded: []LoadedFilament{{Colour: "", Type: "PLA"}},
			want:   []string{"#FFFFFF"},
		},
		{
			// Reported once, so the operator loads one spool rather than reading
			// the same colour twice.
			name:   "a repeated colour is reported once",
			need:   []string{"#1560BD", "#1560BD"},
			loaded: nil,
			want:   []string{"#1560BD"},
		},
		{
			// A white bed needs white for both the body and the lettering, which
			// most machines have - this is the bed that can actually print today.
			name:   "a white bed matches a machine holding white",
			need:   []string{"#FFFFFF", "#FFFFFF"},
			loaded: []LoadedFilament{{Colour: "#F72323"}, white},
			want:   nil,
		},
		{
			name:   "an unreadable colour counts as missing",
			need:   []string{"CHROME"},
			loaded: []LoadedFilament{white, blue},
			want:   []string{"CHROME"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := MissingColours(c.need, c.loaded)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("MissingColours = %v, want %v", got, c.want)
			}
			if HasColours(c.need, c.loaded) != (len(c.want) == 0) {
				t.Errorf("HasColours disagrees with MissingColours for %v", c.need)
			}
		})
	}
}
