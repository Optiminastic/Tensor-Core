package httpapi

// Declaring a design file's coloured pieces by hand, and per colourway.
//
// The reference 3MF beside this is the better route and stays the default: the
// designer has already assigned those colours in the slicer, so reading them
// out of the file beats re-typing them. But it presumes a slicer file exists,
// and a product whose .scad was written before anybody exported one has no way
// in at all - which is where both Diwali products were.
//
// The other half is the colourway. A piece could say "always this hex" or
// "whatever the customer bought", and that covers a plank: white base, the
// customer's colour on the lettering, one piece moving. It does not cover a
// product whose whole palette is designed per variant, like the lotus holder -
// a pink outline on the purple one, gold on the red and the white, and a text
// colour that is different on all three. None of those are the colour the
// customer picked, and none of them are fixed across the product.
//
// So a scheme is addressed by (product, role, variant), where no variant means
// the product's own - the fallback every variant prints from until it declares
// something of its own.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// maxSchemeParts bounds a scheme. A design file prints as two or three pieces;
// anything beyond this is a loop that got away from somebody, and each piece
// costs a render to verify.
const maxSchemeParts = 12

type colourSchemePart struct {
	PartName string `json:"part_name" binding:"required"`
	// ColourHex is "#RRGGBB", or empty for "follow the customer's choice".
	ColourHex string `json:"colour_hex" binding:"omitempty,len=7,startswith=#"`
	// ColourName is the shop's own word for a colour - "gold", "pink" - and is
	// preferred over ColourHex when both arrive.
	//
	// IT IS RESOLVED THE SAME WAY A CUSTOMER'S COLOUR IS, through the operator-
	// confirmed colour map and then the filament shelf, so a fixed piece lands
	// on the spool this shop actually holds. Writing the catalogue value
	// instead is the failure dnp_colour.go was written about: the catalogue
	// calls blue #1560BD and every blue spool in this fleet reports #2850E0,
	// and declaring the catalogue value is what made BambuBuddy refuse plate
	// after plate. A hex typed into a form has exactly that problem.
	ColourName string `json:"colour_name"`
}

type colourSchemeRequest struct {
	// VariantSKU scopes the scheme to one colourway. Empty means the product's
	// own scheme.
	VariantSKU string             `json:"variant_sku"`
	Parts      []colourSchemePart `json:"parts" binding:"required,min=1,dive"`
}

// putColourScheme replaces one (product, role, variant) scheme outright.
//
// REPLACES, like the 3MF upload it sits beside and for the same reason: a piece
// left behind from an earlier scheme is a piece the renderer will ask the
// template for, and a template that no longer draws it answers with nothing and
// holds the job.
func (s *Server) putColourScheme(c *gin.Context) {
	ctx := c.Request.Context()
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	role := strings.TrimSpace(c.Param("role"))
	if role == "" {
		detail(c, http.StatusBadRequest, "Name the design file these pieces belong to.")
		return
	}

	var req colourSchemeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Each piece needs a name, and a colour of the form #RRGGBB or none at all.")
		return
	}
	if len(req.Parts) > maxSchemeParts {
		detail(c, http.StatusUnprocessableEntity,
			"A design file prints as a handful of pieces, not that many.")
		return
	}

	// The variant is resolved against THIS product, so a SKU belonging to
	// another product cannot be given a scheme here. Without that check the
	// scheme would store happily against a variant this page does not describe
	// and silently repaint a different product.
	var variantID *uuid.UUID
	if sku := strings.TrimSpace(req.VariantSKU); sku != "" {
		variant, err := s.store.Q.FindVariantBySKU(ctx, sku)
		if err != nil || variant.ProductID != product.ID {
			detail(c, http.StatusUnprocessableEntity,
				"That SKU is not an active variant of this product.")
			return
		}
		variantID = &variant.ID
	}

	parts := make([]colourPart, 0, len(req.Parts))
	seen := map[string]bool{}
	for _, p := range req.Parts {
		name := strings.TrimSpace(p.PartName)
		if name == "" {
			detail(c, http.StatusUnprocessableEntity, "Every piece needs a name.")
			return
		}
		// Case-insensitively, because that is how the unique index and
		// OpenSCAD's own string comparison both read it. Two rows differing
		// only in case would render one piece twice and the other never.
		if seen[strings.ToLower(name)] {
			detail(c, http.StatusUnprocessableEntity,
				"The piece "+name+" is named twice.")
			return
		}
		seen[strings.ToLower(name)] = true

		// A NAME beats a hex, and is resolved against this shop's spools.
		hex := strings.ToUpper(strings.TrimSpace(p.ColourHex))
		if colour := strings.TrimSpace(p.ColourName); colour != "" {
			resolved, err := s.resolveColourHex(ctx, colour)
			if err != nil {
				// Named rather than silently fixed, because the person
				// configuring this is the one who can add the spool - and a
				// piece quietly painted a guess prints wrong on every order
				// until somebody notices the colour by eye.
				detail(c, http.StatusUnprocessableEntity,
					"No filament matches the colour "+colour+" for the piece "+name+
						". Add it to the colour map or the filament shelf first.")
				return
			}
			hex = resolved
		}
		parts = append(parts, colourPart{Name: name, Hex: hex})
	}

	// Every piece is rendered before any is stored - the same check the 3MF
	// route makes, and for the same reason. A piece whose name the template
	// does not branch on renders an empty solid, which slices and prints and
	// is wrong. One render per piece here costs seconds; finding it later
	// costs a bed.
	if refusal := s.verifyPartsRender(ctx, product.ID, role, parts); refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}

	if err := s.store.Q.ClearDesignColourParts(ctx, gen.ClearDesignColourPartsParams{
		ProductID: product.ID, Role: role, VariantID: variantID,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not replace the colour parts.")
		return
	}

	out := make([]colourPartResponse, 0, len(parts))
	for i, p := range parts {
		var hex *string
		if !p.FollowsCustomer() {
			v := p.Hex
			hex = &v
		}
		row, err := s.store.Q.InsertDesignColourPart(ctx, gen.InsertDesignColourPartParams{
			ID: uuid.New(), ProductID: product.ID, Role: role,
			PartName: p.Name, ColourHex: hex, Position: int32(i),
			VariantID: variantID,
		})
		if err != nil {
			detail(c, http.StatusInternalServerError,
				"Could not store the piece "+p.Name+".")
			return
		}
		res := toColourPartResponse(row)
		res.VariantSKU = strings.TrimSpace(req.VariantSKU)
		out = append(out, res)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}
