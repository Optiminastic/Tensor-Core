package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// The mirror is why this exists: PART="tick" on an order that did not buy a
// tick exits 0 and writes no file, which RenderSTL reports as ErrEmptyModel.
func TestAnOptionalPieceMayRenderNothing(t *testing.T) {
	tick := colourPart{Name: "tick", Hex: "#0095F6", Optional: true}

	if !emptyPartIsFine(tick, personalise.ErrEmptyModel) {
		t.Fatal("an optional piece that drew nothing must be allowed to be absent")
	}
	// Wrapped, which is how it arrives: RenderSTL adds OpenSCAD's last lines.
	wrapped := fmt.Errorf("%w: Current top level object is empty.", personalise.ErrEmptyModel)
	if !emptyPartIsFine(tick, wrapped) {
		t.Fatal("the sentinel must be recognised through a wrap")
	}
	// The other shape of empty: a well-formed STL of no triangles, which the
	// caller reports with a nil error.
	if !emptyPartIsFine(tick, nil) {
		t.Fatal("an optional piece rendering zero triangles must also be allowed")
	}
}

// The guard this feature opts out of is the one that catches a scheme naming a
// piece the template cannot draw. It must keep working for everything else.
func TestARequiredPieceStillHoldsTheJob(t *testing.T) {
	lettering := colourPart{Name: "text"}

	if emptyPartIsFine(lettering, personalise.ErrEmptyModel) {
		t.Fatal("a mandatory piece that drew nothing must still fail - that is a blank plank")
	}
	if emptyPartIsFine(lettering, nil) {
		t.Fatal("a mandatory piece of zero triangles must still fail")
	}
}

// A timeout or a syntax error on the tick is a broken render, not an order
// that did not buy one. Printing the mirror without its badge and reporting
// success is the failure this separates out.
func TestABrokenRenderIsNotAnAbsentPiece(t *testing.T) {
	tick := colourPart{Name: "tick", Optional: true}

	for _, err := range []error{
		errors.New("openscad timed out after 3m0s rendering tsm_wht"),
		context.DeadlineExceeded,
		errors.New("openscad: exit status 1: syntax error"),
	} {
		if emptyPartIsFine(tick, err) {
			t.Fatalf("an optional piece must not swallow %v", err)
		}
	}
}

func mesh(name string, triangles int) meshio.Part {
	return meshio.Part{Name: name, Triangles: make([]orientation.Triangle, triangles)}
}

// The slicer's object list is read by a person, so two renders of the same
// product must not shuffle it.
func TestTheSurvivingPiecesKeepTheirOrder(t *testing.T) {
	// colour, heart, tick, clear - with the tick skipped, as an order without
	// one leaves it.
	out, err := builtMeshes("tsm_wht", []meshio.Part{
		mesh("colour", 120), mesh("heart", 40), {}, mesh("clear", 80),
	})
	if err != nil {
		t.Fatalf("three pieces of four is a model: %v", err)
	}
	var names []string
	for _, m := range out {
		names = append(names, m.Name)
	}
	if fmt.Sprint(names) != "[colour heart clear]" {
		t.Fatalf("pieces came back as %v, want [colour heart clear]", names)
	}
}

// Write3MF would turn this into a valid file of no objects, which slices to an
// empty plate and reports success.
func TestAModelOfNoPiecesIsRefused(t *testing.T) {
	_, err := builtMeshes("tsm_wht", []meshio.Part{{}, {}})
	if err == nil {
		t.Fatal("a model with every piece absent must not be built")
	}
	// Names the template, because the person reading it has to go and look at
	// one specific file.
	if got := err.Error(); got != "tsm_wht rendered nothing for any of its 2 pieces" {
		t.Fatalf("unhelpful refusal: %q", got)
	}
}
