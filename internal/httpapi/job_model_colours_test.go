package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/meshio"
)

func part(name, colour string) meshio.Part {
	return meshio.Part{Name: name, Colour: colour}
}

func TestColoursFromParts(t *testing.T) {
	// The built-in table plus one confirmed spool, which is what a real shop
	// has: #E4002B is Tensor's RED, and this shop has confirmed its own gold.
	byHex := colourNamesFromMap([]colourMapRow{
		{ColourName: "GOLD", Hex: "#C9A227", IsPrimary: true},
	})

	for _, tc := range []struct {
		name         string
		parts        []meshio.Part
		wantNames    []string
		wantUnmapped []string
	}{
		{
			name:      "every colour resolves",
			parts:     []meshio.Part{part("body", "#E4002B"), part("text", "#FFFFFF")},
			wantNames: []string{"RED", "WHITE"},
		},
		{
			// The upload still succeeds; the job keeps saying it has no
			// colour, which is the honest outcome.
			name:         "a colour the shop has never recorded",
			parts:        []meshio.Part{part("body", "#123456")},
			wantUnmapped: []string{"#123456"},
		},
		{
			name:         "one of each",
			parts:        []meshio.Part{part("body", "#E4002B"), part("trim", "#123456")},
			wantNames:    []string{"RED"},
			wantUnmapped: []string{"#123456"},
		},
		{
			name:  "a model that declares no colour at all",
			parts: []meshio.Part{part("body", ""), part("text", "  ")},
		},
		{
			name: "the same colour on several parts counts once",
			parts: []meshio.Part{
				part("a", "#E4002B"), part("b", "#e4002b"), part("c", "#E4002BFF"),
			},
			wantNames: []string{"RED"},
		},
		{
			name:      "a confirmed spool beats the built-in table",
			parts:     []meshio.Part{part("body", "#C9A227")},
			wantNames: []string{"GOLD"},
		},
		{
			// Stable regardless of the order the file lists them in, because
			// the stored value becomes a grouping key.
			name:      "order does not matter",
			parts:     []meshio.Part{part("a", "#FFFFFF"), part("b", "#E4002B")},
			wantNames: []string{"RED", "WHITE"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names, unmapped := coloursFromParts(tc.parts, byHex)
			if !sameStrings(names, tc.wantNames) {
				t.Errorf("names = %v, want %v", names, tc.wantNames)
			}
			if !sameStrings(unmapped, tc.wantUnmapped) {
				t.Errorf("unmapped = %v, want %v", unmapped, tc.wantUnmapped)
			}
		})
	}
}

// THE TEST THIS FILE EXISTS FOR. nearestColourName is one import away and
// would make every one of the cases above "resolve"; it must not be reached
// for. A hex a hair off the shop's red is a different spool, and guessing
// prints somebody's order in the wrong colour with nobody looking.
func TestAnUnknownColourIsNeverGuessedAtTheNearestOne(t *testing.T) {
	byHex := colourNamesFromMap(nil)

	// One bit away from the built-in RED (#E4002B), and far closer to it than
	// to anything else in the table.
	names, unmapped := coloursFromParts([]meshio.Part{part("body", "#E4002C")}, byHex)
	if len(names) != 0 {
		t.Fatalf("a near-miss hex resolved to %v; it must come back unmapped", names)
	}
	if len(unmapped) != 1 || unmapped[0] != "#E4002C" {
		t.Fatalf("unmapped = %v, want the hex itself reported back", unmapped)
	}
}

func TestSkyBlueFoldsToBlue(t *testing.T) {
	// The planner keys a bed on production.NormalisedColourKey, which aliases
	// on the way in. A name stored unfolded would key apart from a job
	// recorded as BLUE and the two would never share a bed.
	byHex := colourNamesFromMap([]colourMapRow{
		{ColourName: "SKY BLUE", Hex: "#4FA8E0", IsPrimary: true},
	})
	names, _ := coloursFromParts([]meshio.Part{part("body", "#4FA8E0")}, byHex)
	if len(names) != 1 || names[0] != "BLUE" {
		t.Errorf("names = %v, want SKY BLUE folded to BLUE", names)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
