package httpapi

// The coloured pieces a design file prints as.
//
// Colour used to be a plank described in Go: every template rendered twice,
// PART="base" painted white and PART="text" painted whatever the customer
// chose. Two pieces, the fixed one always white, and no way to say that a
// rose's heart is red while its stem follows the order.
//
// A product can now declare its pieces instead, read straight out of a
// reference 3MF the designer already builds in the slicer. part_name is both
// the -D PART= value and the object name in that 3MF, so the thing the
// renderer asks for and the thing the designer drew carry one name rather than
// a mapping between two that can drift.
//
// ADDITIVE, like the registry render path it sits beside. A product with no
// parts configured renders exactly as it did before.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// colourPart is one piece of a model: what to ask the template for, and what
// colour the result is painted.
type colourPart struct {
	// Name is the -D PART= value and the name the piece carries in the
	// slicer's object list.
	Name string
	// Hex is "#RRGGBB". Empty means this piece follows the customer's choice,
	// which the caller resolves once for the whole model.
	Hex string
}

// FollowsCustomer reports whether this piece takes the colour off the order.
func (p colourPart) FollowsCustomer() bool { return strings.TrimSpace(p.Hex) == "" }

// defaultColourParts is the plank, expressed as parts.
//
// The behaviour every product had before this existed, and still has when it
// configures nothing: a white base and the customer's colour on the lettering.
// Keeping it as data rather than a branch means the renderer has one shape, and
// the configured path cannot drift from the one that prints today.
func defaultColourParts() []colourPart {
	return []colourPart{
		{Name: personalise.PartBase, Hex: BasePlateColour},
		{Name: personalise.PartText},
	}
}

// colourPartsForJob is how this job's model is divided into colours.
//
// Falls back to the plank's two parts for anything the registry does not
// describe. Deliberately quiet about WHY it fell back: an unconfigured product
// and an unreachable registry both mean "render it the way it has always been
// rendered", and the alternative - failing a live job because a lookup for an
// optional feature errored - is worse than printing what it printed yesterday.
func (s *Server) colourPartsForJob(ctx context.Context, job gen.ProductionJob) []colourPart {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return defaultColourParts()
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return defaultColourParts()
	}
	rows, err := s.store.Q.ListDesignColourParts(ctx, gen.ListDesignColourPartsParams{
		ProductID: product.ID, Role: partRoleOf(job),
	})
	if err != nil || len(rows) == 0 {
		return defaultColourParts()
	}

	parts := make([]colourPart, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, colourPart{Name: r.PartName, Hex: deref(r.ColourHex)})
	}
	return parts
}

// needsCustomerColour reports whether any piece takes its colour off the order.
//
// Asked before the colour is resolved, so a model whose pieces are all fixed
// does not fail on a variant title nobody needed. A rose that is red whatever
// the customer picked should not be held because "RED SPARKLE" is not on the
// filament shelf.
func needsCustomerColour(parts []colourPart) bool {
	for _, p := range parts {
		if p.FollowsCustomer() {
			return true
		}
	}
	return false
}

// partsFrom3MF reads the pieces and colours out of a reference model.
//
// The designer's own file is the specification. They have already assigned
// these colours in the slicer, and re-typing them into a form is both work and
// a chance to get one wrong.
//
// An object with no material comes back with an empty colour, which this maps
// to "follows the customer" - the common case by far, since the piece carrying
// the customer's colour is exactly the one a reference file cannot state.
func partsFrom3MF(parts []threeMFPart) ([]colourPart, error) {
	out := make([]colourPart, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, fmt.Errorf(
				"one of the objects in that file has no name, and the name is what " +
					"the template is asked for - name every object in the slicer first")
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf(
				"that file has two objects called %q; each piece needs its own name "+
					"because the name is what the template is asked for", name)
		}
		seen[strings.ToLower(name)] = true
		out = append(out, colourPart{Name: name, Hex: strings.TrimSpace(p.Colour)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("that file contains no objects")
	}
	return out, nil
}

// threeMFPart is the shape partsFrom3MF needs, so the parsing above can be
// tested without building a zip archive.
type threeMFPart struct {
	Name   string
	Colour string
}
