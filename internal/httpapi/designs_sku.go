package httpapi

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// skuPattern is the accepted SKU shape: starts alphanumeric, then letters,
// digits, and the separators - . _ / , up to 64 characters. Deliberately
// permissive so a shop's own scheme (e.g. GF-LAMP-001-PLA-WHT-V1) is accepted;
// uniqueness, not format, is the real constraint (the designs_sku_key index).
var skuPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)

// skuRequest is the PATCH /designs/:id/sku body. An empty string clears the SKU.
type skuRequest struct {
	SKU string `json:"sku"`
}

// setDesignSku assigns (or clears) a design's catalog SKU. The SKU is what an
// incoming order resolves against, so it is guarded by shopify:publish - the same
// Project-Lead authority that lists the design on Shopify. A duplicate SKU is a
// 409; the DB partial-unique index is the source of truth, not a pre-check.
func (s *Server) setDesignSku(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req skuRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'sku' field is required.")
		return
	}

	var skuArg *string
	if trimmed := strings.TrimSpace(req.SKU); trimmed != "" {
		if !skuPattern.MatchString(trimmed) {
			detail(c, http.StatusUnprocessableEntity,
				"A SKU may contain letters, digits and - . _ / , up to 64 characters.")
			return
		}
		skuArg = &trimmed
	}

	row, err := s.store.Q.SetDesignSku(c.Request.Context(), gen.SetDesignSkuParams{ID: id, Sku: skuArg})
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "That design does not exist.")
			return
		}
		if isUniqueViolation(err) {
			detail(c, http.StatusConflict, "That SKU is already used by another design.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not update the SKU.")
		return
	}
	c.JSON(http.StatusOK, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct,
		row.PreviewKey, row.Sku, row.CreatedAt, row.UpdatedAt))
}

// renameRequest is the PATCH /designs/:id/name body.
type renameRequest struct {
	Name string `json:"name"`
}

// renameDesign changes a design's display name. Guarded by design:update; the
// /:id brand-access gate already 404s a design the caller cannot reach.
func (s *Server) renameDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req renameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'name' field is required.")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 160 {
		detail(c, http.StatusUnprocessableEntity, "Name must be 1 to 160 characters.")
		return
	}
	if err := s.store.Q.SetDesignName(c.Request.Context(), gen.SetDesignNameParams{ID: id, Name: name}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not rename the design.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id.String(), "name": name})
}
