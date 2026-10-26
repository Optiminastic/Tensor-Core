package httpapi

// Which mapping drives one SKU's design file.
//
// A mapping says "the customer's Your name feeds NAME", and for most products
// it says it once, for the whole product: three colourways of one drawing
// declare the same variable, so one row covers them all.
//
// A product whose child SKUs carry their own .scad files is the case that
// breaks that. Those files are separate drawings - an assembled Royal holder
// may call the customer's text NAME where the plate calls it TEXT - and
// forcing one variable name across them refuses a correct file over a
// spelling.
//
// So a variant may declare its own, and the rule for choosing between them is
// the same one resolveScheme applies to colours, for the same reason.

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// fieldMapsFor is the mapping this SKU's file is driven by.
//
// ALL OR NOTHING. A variant that maps anything maps everything; the product's
// mapping is what a variant that maps nothing falls back to.
//
// Merging them is the tempting alternative and it is wrong in the way that
// does not announce itself. A variant that mapped only its second field would
// have its first filled from the product's row, pointing at a variable its
// own file does not declare - and OpenSCAD accepts an unknown -D without
// complaint, so the model would render, slice, print, and simply have no name
// on it. Replacing outright means a half-written mapping is visibly half
// written.
//
// An empty result means "not configured", which every caller already treats
// as "this product is not the registry's to render".
func (s *Server) fieldMapsFor(
	ctx context.Context, productID uuid.UUID, sku, role string,
) []gen.ProductFieldMap {
	if variant, err := s.store.Q.FindVariantBySKU(ctx, sku); err == nil {
		own, err := s.store.Q.ListVariantFieldMaps(ctx, gen.ListVariantFieldMapsParams{
			VariantID: &variant.ID, Role: role,
		})
		if err == nil && len(own) > 0 {
			return own
		}
	}
	shared, err := s.store.Q.ListProductFieldMaps(ctx, gen.ListProductFieldMapsParams{
		ProductID: productID, Role: role,
	})
	if err != nil {
		return nil
	}
	return shared
}

// toFieldMaps is the stored rows as the renderer's mapping.
//
// One conversion, shared by the production render and the preview, so the two
// cannot read the same rows differently - which is the only way the name on
// the picture and the name on the plate could come apart.
func toFieldMaps(rows []gen.ProductFieldMap) []personalise.FieldMap {
	maps := make([]personalise.FieldMap, 0, len(rows))
	for _, r := range rows {
		maps = append(maps, personalise.FieldMap{
			PropertyKey:  r.PropertyKey,
			ScadVariable: r.ScadVariable,
			Kind:         personalise.KindFor(r.ValueType),
			Optional:     !r.Required,
			Fixed:        deref(r.FixedValue),
			Default:      deref(r.DefaultValue),
		})
	}
	return maps
}
