package httpapi

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The Family Heart: its .scad declares a white body and a red top, and the
// order carries names only. The job takes its colours from the model.
func TestModelColoursAreTheDistinctPartHexes(t *testing.T) {
	got := modelColours([]meshio.Part{
		{Name: "body", Colour: "#ffffff"}, {Name: "top", Colour: "#C12E1E"}, {Name: "rim", Colour: "#FFFFFF"},
	})
	if strings.Join(got, ",") != "#FFFFFF,#C12E1E" {
		t.Fatalf("colours = %v", got)
	}
	if key := production.NormalisedColourKey(got); key != "#C12E1E+#FFFFFF" {
		t.Fatalf("planner key = %q", key)
	}
}

func TestAColourWrittenAsAHexIsItsOwnSwatch(t *testing.T) {
	if hex, ok := pickColourHex(nil, nil, production.CanonicalColourName("#c12e1e")); !ok || hex != "#C12E1E" {
		t.Fatalf("hex = %q, %v", hex, ok)
	}
	// A name still has to be known: nothing guesses a swatch for a word.
	if _, ok := pickColourHex(nil, nil, "SUNSET ORANGE"); ok {
		t.Fatal("an unknown name resolved")
	}
}
