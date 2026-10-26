package httpapi

// Assigning the file the customer is shown.
//
// The pieces of that file and their colours go through the colour-scheme route
// beside this, with role "preview" - it already takes a role and needed no
// change. What it cannot do is say WHICH FILE those pieces come from, because
// a design file is bound to a variant, not to a scheme. That is this.
//
// Per variant, so a product whose colourways are different shapes can name a
// file for each, and assignable to every variant at once, because the usual
// case is three colourways of one shape sharing one file.

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

func (s *Server) registerRegistryPreviewDesign(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/products/:code/preview-design", read, s.listPreviewDesigns)
	g.PUT("/products/:code/preview-design", manage, s.putPreviewDesign)
	g.DELETE("/products/:code/preview-design", manage, s.clearPreviewDesign)
}

type previewDesignResponse struct {
	// VariantSKU is the colourway this file is assigned to.
	VariantSKU string `json:"variant_sku"`
	// TemplateKey is the .scad the picture is drawn from, or empty when this
	// variant has none and its picture is a picture of what prints.
	TemplateKey string `json:"template_key"`
}

// listPreviewDesigns is which assembled file each colourway is drawn from.
func (s *Server) listPreviewDesigns(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.ListDesignsForProduct(c.Request.Context(), product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the preview files.")
		return
	}
	out := make([]previewDesignResponse, 0, len(rows))
	for _, r := range rows {
		if !isPreviewRole(r.Role) {
			continue
		}
		out = append(out, previewDesignResponse{
			VariantSKU: deref(r.Sku), TemplateKey: strings.TrimSpace(deref(r.TemplateKey)),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type previewDesignRequest struct {
	// TemplateKey is the assembled .scad, already uploaded through the
	// templates route. Uploading is a separate act from assigning so there is
	// one way a .scad enters Tensor.
	TemplateKey string `json:"template_key" binding:"required"`
	// VariantSKU narrows it to one colourway. Empty assigns it to every
	// active variant, which is the usual case.
	VariantSKU string `json:"variant_sku"`
}

// putPreviewDesign points one colourway, or all of them, at an assembled file.
func (s *Server) putPreviewDesign(c *gin.Context) {
	ctx := c.Request.Context()
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	var req previewDesignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Name the design file to draw the preview from.")
		return
	}
	key := strings.TrimSpace(req.TemplateKey)
	if !s.templateExists(ctx, key) {
		detail(c, http.StatusUnprocessableEntity,
			"There is no design file called "+key+". Upload it on the Designs tab first.")
		return
	}

	// THE PICTURE IS DRIVEN BY THE PLATE'S FIELD MAPPING, so a file that does
	// not declare those variables draws a lotus with no name on it - and does
	// it silently, because OpenSCAD accepts an unknown -D without complaint.
	// Refused here, while the person who uploaded it is still present, rather
	// than discovered on a live order.
	// Checked against the mapping the SKU being assigned actually uses. With
	// no SKU named the file goes to every colourway, and the product's
	// mapping is the one they share.
	if refusal := s.previewDeclaresMappedFields(
		ctx, product.ID, key, strings.TrimSpace(req.VariantSKU),
	); refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}

	// AND IT HAS TO DRAW THE PIECES THIS PRODUCT ALREADY EXPECTS.
	//
	// The pieces are verified when they are SAVED, against the file assigned
	// at that moment. Replacing the file afterwards would check nothing, and
	// a file that does not branch on "diya_placed" answers that request with
	// an empty solid - so every order's picture would quietly lose its cup.
	//
	// Nothing to check when the pieces have not been configured yet, which is
	// the order somebody adding a new product works in.
	if parts := s.previewSchemeFor(ctx, product.ID); len(parts) > 0 {
		if refusal := s.verifyPartsRenderFrom(ctx, key, parts); refusal != "" {
			detail(c, http.StatusUnprocessableEntity, refusal)
			return
		}
	}

	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read this product's colourways.")
		return
	}
	wanted := strings.TrimSpace(req.VariantSKU)
	assigned := 0
	for i := range variants {
		if variants[i].Status == "retired" {
			continue
		}
		if wanted != "" && !strings.EqualFold(deref(variants[i].Sku), wanted) {
			continue
		}
		// Replaced, not added to: two active preview rows for one variant
		// would make previewTemplateFor answer whichever the database
		// returned first, so the picture would change between renders of the
		// same order.
		if err := s.store.Q.SupersedeVariantDesign(ctx, gen.SupersedeVariantDesignParams{
			VariantID: variants[i].ID, Role: designRolePreview,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not replace the existing preview file.")
			return
		}
		if _, err := s.store.Q.InsertVariantDesign(ctx, gen.InsertVariantDesignParams{
			ID: uuid.New(), VariantID: variants[i].ID, Role: designRolePreview,
			TemplateKey: &key, Version: 1, Status: "active",
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not assign the preview file.")
			return
		}
		assigned++
	}
	if assigned == 0 {
		detail(c, http.StatusUnprocessableEntity,
			"That SKU is not an active colourway of this product.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"assigned": assigned, "template_key": key})
}

// previewSchemeFor is every piece this product's picture is made of.
//
// Across variants as well as the product's own scheme, because a file is
// assigned to variants and has to draw whatever any of them asks it for. A
// piece listed by one colourway and missing from the file is as broken as one
// listed by all of them.
func (s *Server) previewSchemeFor(ctx context.Context, productID uuid.UUID) []colourPart {
	rows, err := s.store.Q.ListAllDesignColourParts(ctx, productID)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]colourPart, 0, len(rows))
	for _, r := range rows {
		if !isPreviewRole(r.Role) {
			continue
		}
		// One render per distinct piece NAME: the same name in three
		// colourways is the same request to OpenSCAD, and rendering it three
		// times would treble the wait for no extra answer.
		name := strings.ToLower(r.PartName)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, colourPart{Name: r.PartName, Hex: deref(r.ColourHex)})
	}
	return out
}

// previewDeclaresMappedFields checks the assembled file against the mapping
// the plate is driven by, and names what is missing.
//
// Empty means it is fine, or that nothing could be checked - no renderer, no
// mapping yet - because configuring in either order is reasonable and a gate
// that cannot see the file must not refuse it.
func (s *Server) previewDeclaresMappedFields(
	ctx context.Context, productID uuid.UUID, key, sku string,
) string {
	// ITS OWN MAPPING WHEN IT HAS ONE.
	//
	// The preview file may name the customer's text differently from the
	// printed design - an assembled Royal holder calling it NAME where the
	// plate calls it TEXT - and that is a spelling, not a disagreement: both
	// rows read the same order property, so both models carry the same
	// string. Checking such a file against the plate's variable names would
	// refuse a correct file.
	//
	// The plate's mapping is the fallback, which is the common case: an
	// assembled file usually declares the same variables, and requiring a
	// second mapping for it would be a second thing to keep in step.
	rows := s.fieldMapsFor(ctx, productID, sku, designRolePreview)
	if len(rows) == 0 {
		rows = s.fieldMapsFor(ctx, productID, sku, designRoleBody)
	}
	if len(rows) == 0 {
		return ""
	}
	source, ok, err := s.templateLoader()(ctx, key)
	if err != nil || !ok || len(source) == 0 {
		// An embedded template, or one whose bytes could not be fetched.
		// Neither is a reason to refuse an assignment: the first is a file
		// this check cannot read and the second is a transient failure, and
		// refusing on either would block configuring a product because
		// object storage blinked.
		return ""
	}
	wanted := make([]string, 0, len(rows))
	for _, r := range rows {
		// A fixed value never comes off the order, so the preview does not
		// need to read it.
		if strings.TrimSpace(deref(r.FixedValue)) != "" {
			continue
		}
		wanted = append(wanted, r.ScadVariable)
	}
	missing := personalise.DeclaresAll(source, wanted)
	if len(missing) == 0 {
		return ""
	}
	return key + " does not declare " + strings.Join(missing, ", ") +
		" - map those variables for the preview on the Design & fields panel, or " +
		"name them in the file as the printed design does. OpenSCAD accepts a " +
		"variable it does not have without complaint, so the picture would simply " +
		"be drawn without them."
}

// clearPreviewDesign drops the assembled file, for one colourway or all.
//
// The picture then becomes a picture of what prints, which is what every
// product without one already shows.
func (s *Server) clearPreviewDesign(c *gin.Context) {
	ctx := c.Request.Context()
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read this product's colourways.")
		return
	}
	wanted := strings.TrimSpace(c.Query("variant_sku"))
	for i := range variants {
		if wanted != "" && !strings.EqualFold(deref(variants[i].Sku), wanted) {
			continue
		}
		if err := s.store.Q.SupersedeVariantDesign(ctx, gen.SupersedeVariantDesignParams{
			VariantID: variants[i].ID, Role: designRolePreview,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not remove the preview file.")
			return
		}
	}
	c.Status(http.StatusNoContent)
}
