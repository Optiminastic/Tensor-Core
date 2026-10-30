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
	"github.com/google/uuid"

	"context"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
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

	role := partRoleOf(job)

	// THE FILE'S OWN COLOURS FIRST, when it states them.
	//
	// A flagged file names its pieces and paints them in its own source, so
	// there is nothing configured for this SKU to resolve - that is the whole
	// point of flagging it. Asked before the schemes rather than after,
	// because a half-configured leftover scheme would otherwise win over the
	// file that is actually being rendered.
	//
	// Still one piece per declaration, and still one render pass each:
	// Write3MF needs a hex per object, so this changes where the colours come
	// from and nothing about how the model is built.
	if template, ok := s.templateForVariant(ctx, product.ID, sku, role); ok {
		if parts := s.selfColouredParts(ctx, template); len(parts) > 0 {
			return parts
		}
	}

	// The VARIANT's own scheme first, because a product may paint each
	// colourway differently. A SKU that resolves to a product but not to a
	// variant cannot happen - FindProductBySKU reaches the product THROUGH the
	// variant on identical terms - and a lookup that fails here simply leaves
	// the variant with no scheme, which falls back to the product's.
	var own []colourPart
	if variantID := s.variantForColourway(ctx, product.ID, sku, job); variantID != nil {
		rows, err := s.store.Q.ListColourPartsForVariant(ctx,
			gen.ListColourPartsForVariantParams{VariantID: variantID, Role: role})
		if err == nil {
			own = toColourParts(rows)
		}
	}

	rows, err := s.store.Q.ListDesignColourParts(ctx, gen.ListDesignColourPartsParams{
		ProductID: product.ID, Role: role,
	})
	var shared []colourPart
	if err == nil {
		shared = toColourParts(rows)
	}

	parts := resolveScheme(own, shared)
	if len(parts) == 0 {
		return defaultColourParts()
	}
	return parts
}

// variantForColourway is the variant whose palette this job should print in.
//
// THE COLOUR THE CUSTOMER WILL RECEIVE, NOT THE ONE THEY BOUGHT.
//
// A per-variant scheme is a colourway: the Lotus is purple-with-pink, or
// red-with-gold, or white-with-gold. Looking it up by SKU was right only while
// nobody could change their mind - and the personalisation link exists so they
// can. A customer who bought PWDH-P and chose WHITE had the purple scheme
// rendered anyway, because the SKU still said purple. The model came back
// purple-and-pink with a gold name on it, and nothing anywhere disagreed.
//
// So the chosen colour picks the variant, and the SKU is only the fallback for
// a job nobody has changed.
func (s *Server) variantForColourway(
	ctx context.Context, productID uuid.UUID, sku string, job gen.ProductionJob,
) *uuid.UUID {
	bySKU, skuErr := s.store.Q.FindVariantBySKU(ctx, sku)

	chosen := strings.TrimSpace(deref(job.PersonalisationColour))
	if chosen == "" {
		// Unchanged: the variant they bought is the colourway they get.
		if skuErr != nil {
			return nil
		}
		return &bySKU.ID
	}

	variants, err := s.store.Q.ListVariantsForProduct(ctx, productID)
	if err == nil {
		if match := matchVariantByColour(variants, chosen); match != nil {
			return match
		}
	}
	// A colour this product does not sell as a variant - which the picker no
	// longer offers, but an older job may carry. The bought variant's scheme
	// is a better answer than none: its pieces are at least the right shape,
	// and the piece that follows the customer still takes their colour.
	if skuErr != nil {
		return nil
	}
	return &bySKU.ID
}

// skuForColourway is the SKU whose FILES this job should be built from.
//
// THE COLOUR THEY CHOSE, NOT THE ONE THEY BOUGHT - the same rule
// variantForColourway applies to the palette, applied to the drawing.
//
// It had to be applied twice because a colourway can be expressed two ways. A
// Lotus is one file plus a scheme per variant, so re-resolving the SCHEME was
// enough. A Royal holder is a file per child SKU, each already in its own
// colours and declaring them itself - so for that product the file IS the
// colour, and a scheme that followed the customer while the file did not left
// the customer looking at the colourway they bought however they changed
// their mind. Red, saved, still purple on screen.
//
// The printed model resolved its file the same way, which is the half that
// mattered: a customer could approve a red holder and be sent a purple one,
// with the order, the picture and the model all agreeing it was purple.
//
// Falls back to the SKU on the job, which is the answer whenever nobody has
// changed anything and whenever the chosen colour is not a variant of this
// product.
func (s *Server) skuForColourway(
	ctx context.Context, productID uuid.UUID, sku string, job gen.ProductionJob,
) string {
	variantID := s.variantForColourway(ctx, productID, sku, job)
	if variantID == nil {
		return sku
	}
	variant, err := s.store.Q.GetVariant(ctx, *variantID)
	if err != nil {
		return sku
	}
	if chosen := strings.TrimSpace(deref(variant.Sku)); chosen != "" {
		return chosen
	}
	return sku
}

// matchVariantByColour finds the variant named for a colour, or nil.
//
// Canonicalised on both sides, because "SKY BLUE" and "Blue" are one spool and
// the storefront is not consistent about case.
func matchVariantByColour(
	variants []gen.ListVariantsForProductRow, chosen string,
) *uuid.UUID {
	want := production.CanonicalColourName(chosen)
	if want == "" {
		return nil
	}
	for i := range variants {
		if variants[i].Status == "retired" {
			continue
		}
		if production.CanonicalColourName(variants[i].Name) == want {
			return &variants[i].ID
		}
	}
	return nil
}

// toColourParts is the stored rows as the renderer's pieces.
func toColourParts(rows []gen.DesignColourPart) []colourPart {
	parts := make([]colourPart, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, colourPart{Name: r.PartName, Hex: deref(r.ColourHex)})
	}
	return parts
}

// resolveScheme picks the scheme one variant prints from.
//
// ALL OR NOTHING, and that is the whole rule. A variant that declares a scheme
// declares every piece of it; the product's scheme is what a variant that
// declares nothing falls back to.
//
// The tempting alternative is to merge - take the variant's pieces and fill the
// gaps from the product - and it is wrong in the one way that does not announce
// itself. The lotus holder's purple scheme names a pink outline and a gold
// text; its red scheme names a gold outline and a yellow text. A variant that
// named only its outline would be completed from whichever scheme happened to
// sit on the product, print a palette that exists in neither design, and slice
// and print without complaint. Replacing outright means a half-written scheme
// is visibly half-written.
func resolveScheme(own, shared []colourPart) []colourPart {
	if len(own) > 0 {
		return own
	}
	return shared
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
