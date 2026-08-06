package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxNameRuleLength caps a product's configurable name limit - a sanity bound so
// a rule set can never demand an absurd engraving length.
const maxNameRuleLength = 200

// personalisationRulesRequest is the PATCH /designs/:id/personalisation-rules
// body. A null (or omitted) rules object clears personalisation for the product.
type personalisationRulesRequest struct {
	Rules *production.PersonalisationRules `json:"rules"`
}

// setDesignPersonalisationRules stores what personalisation a product offers. It
// is the product-definition counterpart to the SKU write, so it is guarded by the
// same Project-Lead permission (shopify:publish). The backend validates the rule
// shape; an order's personalisation is later checked against it at job creation.
func (s *Server) setDesignPersonalisationRules(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req personalisationRulesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'rules' object is required.")
		return
	}

	var raw []byte
	if req.Rules != nil {
		if msg := validatePersonalisationRules(*req.Rules); msg != "" {
			detail(c, http.StatusUnprocessableEntity, msg)
			return
		}
		encoded, err := json.Marshal(req.Rules)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not encode the rules.")
			return
		}
		raw = encoded
	}

	row, err := s.store.Q.SetDesignPersonalisationRules(c.Request.Context(),
		gen.SetDesignPersonalisationRulesParams{ID: id, PersonalisationRules: raw})
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "That design does not exist.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not save the personalisation rules.")
		return
	}
	c.JSON(http.StatusOK, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct,
		row.PreviewKey, row.Sku, row.CreatedAt, row.UpdatedAt))
}

// validatePersonalisationRules returns a human-readable message when a rule set is
// nonsensical, or "" when it is acceptable. It only guards obviously-wrong shapes;
// the field semantics are enforced by production.ValidateAgainstRules at runtime.
func validatePersonalisationRules(r production.PersonalisationRules) string {
	if r.Name.MaxLength < 0 || r.Name.MinLength < 0 {
		return "Name length limits cannot be negative."
	}
	if r.Name.MaxLength > maxNameRuleLength {
		return fmt.Sprintf("Name limit cannot exceed %d characters.", maxNameRuleLength)
	}
	if r.Name.MaxLength > 0 && r.Name.MinLength > r.Name.MaxLength {
		return "The minimum name length cannot exceed the maximum."
	}
	if r.Zone != nil && (r.Zone.WidthMM < 0 || r.Zone.HeightMM < 0) {
		return "Zone dimensions cannot be negative."
	}
	return ""
}
