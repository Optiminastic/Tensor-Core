package httpapi

// Whether a SKU's customer is asked before it prints.
//
// Every personalised product asks today, decided entirely by what the ORDER
// carries: a line with a name on it gets a link and a thirty-minute hold.
// That is right for the plank range and wrong as a universal rule. A product
// whose design cannot change after it is bought, or one whose preview is not
// ready for customers yet, should go to the floor without interrupting
// anybody - and until now the only way to say so was to switch the feature
// off for the whole brand.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type variantPersonalisationLinkRequest struct {
	// Enabled is a pointer so "not mentioned" is distinguishable from
	// "false". A PATCH body that forgot the field must not silently switch a
	// product's link off.
	Enabled *bool `json:"enabled" binding:"required"`
}

// setVariantPersonalisationLink turns one SKU's customer check on or off.
func (s *Server) setVariantPersonalisationLink(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req variantPersonalisationLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		detail(c, http.StatusUnprocessableEntity,
			"Say whether this SKU should ask its customer: enabled true or false.")
		return
	}

	row, err := s.store.Q.SetVariantPersonalisationLink(c.Request.Context(),
		gen.SetVariantPersonalisationLinkParams{ID: id, Enabled: *req.Enabled})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "No such SKU.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not change that setting.")
		return
	}

	// NOTHING IS SAID ABOUT ORDERS ALREADY HELD, deliberately.
	//
	// Switching this off does not release a customer who has the page open in
	// front of them: personalisationWillAsk checks for a live link FIRST and
	// outranks this setting, so an order mid-window finishes its window. The
	// setting decides whether to START holding, never whether to release.
	c.JSON(http.StatusOK, gin.H{
		"id":                   row.ID.String(),
		"sku":                  row.Sku,
		"personalisation_link": row.PersonalisationLinkEnabled,
	})
}
