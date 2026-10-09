package httpapi

// What a model is divided into, and which piece takes the order's colour.
//
// These are the rules that decide what comes out of a printer, so they are
// pinned rather than trusted: the failure they guard against is not a crash
// but a model that slices, prints, and is the wrong colour.

import (
	"reflect"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

func TestAProductThatConfiguresNothingStillPrintsAPlank(t *testing.T) {
	// The behaviour every product had before colour was configurable. If this
	// ever changes, every plank printing today changes with it.
	got := defaultColourParts()
	want := []colourPart{
		{Name: personalise.PartBase, Hex: BasePlateColour},
		{Name: personalise.PartText},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default parts = %#v, want %#v", got, want)
	}
	if !got[1].FollowsCustomer() {
		t.Error("the lettering must follow the customer; a fixed colour here prints every order the same")
	}
	if got[0].FollowsCustomer() {
		t.Error("the base must stay white, not take the customer's colour")
	}
}

func TestOnlyAModelThatAsksForTheCustomersColourNeedsOne(t *testing.T) {
	allFixed := []colourPart{{Name: "stem", Hex: "#1B7F3B"}, {Name: "heart", Hex: "#C8102E"}}
	if needsCustomerColour(allFixed) {
		// Otherwise a rose that is red whatever the customer picked gets held
		// because "RED SPARKLE" is not on the filament shelf.
		t.Error("a model of fixed colours must not demand a resolvable order colour")
	}
	if !needsCustomerColour([]colourPart{{Name: "body", Hex: "#FFFFFF"}, {Name: "letters"}}) {
		t.Error("a piece with no colour of its own takes the customer's and must say so")
	}
}

func TestTheReferenceModelNamesThePiecesAndTheirColours(t *testing.T) {
	got, err := partsFrom3MF([]threeMFPart{
		{Name: "body", Colour: "#FFFFFF"},
		{Name: "letters"}, // no material: this is the one the customer colours
		{Name: "heart", Colour: "#C8102E"},
	})
	if err != nil {
		t.Fatalf("read parts: %v", err)
	}
	want := []colourPart{
		{Name: "body", Hex: "#FFFFFF"},
		{Name: "letters"},
		{Name: "heart", Hex: "#C8102E"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts = %#v, want %#v", got, want)
	}
	// Order is the file's order, because that is the order the designer sees
	// in the slicer and the order the editor will show.
	if got[0].Name != "body" || got[2].Name != "heart" {
		t.Error("the file's own order was not preserved")
	}
}

func TestAnUnnamedOrDuplicatedObjectIsRefused(t *testing.T) {
	// The name IS the -D PART= value. An unnamed object cannot be asked for,
	// and two objects sharing a name would render one piece twice and the
	// other never - which slices and prints.
	for _, tc := range []struct {
		name  string
		parts []threeMFPart
	}{
		{"no name", []threeMFPart{{Name: "body", Colour: "#FFFFFF"}, {Name: "  "}}},
		{"same name twice", []threeMFPart{{Name: "body"}, {Name: "BODY"}}},
		{"nothing at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := partsFrom3MF(tc.parts); err == nil {
				t.Fatal("want a refusal naming the problem, got none")
			}
		})
	}
}
