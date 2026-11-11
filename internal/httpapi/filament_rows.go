package httpapi

// Editing and deleting one filament row by id - the row menu on the Inventory
// page. The POST in filament.go is keyed on (material, colour), which is right
// for "restock this" and cannot correct a misspelt colour.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type filamentEditRequest struct {
	Material string  `json:"material" binding:"required,min=1,max=255"`
	Colour   *string `json:"colour"`
	// ColourHex is the dot beside the name. Blank works it out from the colour
	// name, the same way a render does; a name nothing knows gets none.
	ColourHex         *string `json:"colour_hex"`
	GramsAvailable    float64 `json:"grams_available" binding:"gte=0"`
	ReorderLevelGrams float64 `json:"reorder_level_grams" binding:"gte=0"`
}

func (s *Server) updateFilamentRow(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid filament id.")
		return
	}
	var req filamentEditRequest
	if !bindJSON(c, &req) {
		return
	}
	colour := nonEmptyPtr(strings.TrimSpace(deref(req.Colour)))
	var swatch *string
	if raw := strings.TrimSpace(deref(req.ColourHex)); raw != "" {
		hex, ok := normaliseHex(raw)
		if !ok {
			detail(c, http.StatusUnprocessableEntity, "The colour code must look like #1A1A1A.")
			return
		}
		swatch = &hex
	} else if hex, err := s.resolveColourHex(c.Request.Context(), deref(colour)); err == nil {
		swatch = &hex
	}

	row, err := s.store.Q.UpdateFilamentRow(c.Request.Context(), gen.UpdateFilamentRowParams{
		ID: id, Material: strings.TrimSpace(req.Material), Colour: colour, ColourHex: swatch,
		GramsAvailable: req.GramsAvailable, ReorderLevelGrams: req.ReorderLevelGrams,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That filament row no longer exists.")
		return
	}
	if isUniqueViolation(err) {
		// Two rows for one material and colour would split its stock in two.
		detail(c, http.StatusConflict, "That material and colour is already on the shelf.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the filament.")
		return
	}
	c.JSON(http.StatusOK, filamentDTO(row))
}

func (s *Server) deleteFilamentRow(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid filament id.")
		return
	}
	if _, err := s.store.Q.DeleteFilamentRow(c.Request.Context(), id); isNoRows(err) {
		detail(c, http.StatusNotFound, "That filament row no longer exists.")
		return
	} else if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the filament.")
		return
	}
	c.Status(http.StatusNoContent)
}
