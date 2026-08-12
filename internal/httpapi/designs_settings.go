package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// uploadAttrKeys are the upload-metadata attribute keys the Settings editor owns.
// The merge below replaces only these, so a design's other attributes (e.g. the
// shopify_* import snapshot) survive an attributes edit.
var uploadAttrKeys = []string{
	"product_type", "personalisation_type", "packaging_type", "colour_count", "add_ons",
}

// notesRequest is the PATCH /designs/:id/notes body. An empty string clears the notes.
type notesRequest struct {
	Notes string `json:"notes"`
}

// setDesignNotes updates the designer notes shown to the Project Lead. Guarded by
// design:update; the /:id brand-access gate already 404s an unreachable design.
func (s *Server) setDesignNotes(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req notesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'notes' field is required.")
		return
	}
	notes := strings.TrimSpace(req.Notes)
	if len([]rune(notes)) > 2000 {
		detail(c, http.StatusUnprocessableEntity, "Notes must be 2000 characters or fewer.")
		return
	}
	var arg *string
	if notes != "" {
		arg = &notes
	}
	if err := s.store.Q.SetDesignNotes(c.Request.Context(), gen.SetDesignNotesParams{ID: id, Notes: arg}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the notes.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id.String()})
}

// attributesRequest is the PATCH /designs/:id/attributes body: the upload-metadata
// fields. Empty fields clear that attribute.
type attributesRequest struct {
	ProductType         string   `json:"product_type"`
	PersonalisationType string   `json:"personalisation_type"`
	ColourCount         int      `json:"colour_count"`
	AddOns              []string `json:"add_ons"`
	PackagingType       string   `json:"packaging_type"`
}

// setDesignAttributes replaces the upload-metadata attributes, preserving any
// other keys in the jsonb (e.g. the Shopify-import snapshot). Guarded by
// design:update.
func (s *Server) setDesignAttributes(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req attributesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with the attribute fields is required.")
		return
	}
	d, err := s.store.Q.GetDesignByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	attrs := map[string]any{}
	if len(d.Attributes) > 0 {
		_ = json.Unmarshal(d.Attributes, &attrs)
	}
	for _, k := range uploadAttrKeys {
		delete(attrs, k)
	}
	if v := trimMax(req.ProductType, 80); v != "" {
		attrs["product_type"] = v
	}
	if v := trimMax(req.PersonalisationType, 80); v != "" {
		attrs["personalisation_type"] = v
	}
	if v := trimMax(req.PackagingType, 80); v != "" {
		attrs["packaging_type"] = v
	}
	if req.ColourCount > 0 && req.ColourCount <= 20 {
		attrs["colour_count"] = req.ColourCount
	}
	if addOns := cleanStrings(req.AddOns); len(addOns) > 0 {
		attrs["add_ons"] = addOns
	}

	var out []byte
	if len(attrs) > 0 {
		out, _ = json.Marshal(attrs)
	}
	if err := s.store.Q.SetDesignAttributes(c.Request.Context(), gen.SetDesignAttributesParams{ID: id, Attributes: out}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the attributes.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id.String()})
}

type descriptionRequest struct {
	Description string `json:"description"`
}

const maxProductDescription = 8000

// setDesignDescription stores the product marketing description on the design (in
// the attributes bag, preserving every other key). The publish dialog pre-fills
// from it. Guarded by design:content - the Marketing Head's one write. An empty
// body clears the description.
func (s *Server) setDesignDescription(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req descriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A JSON body with a 'description' field is required.")
		return
	}
	d, err := s.store.Q.GetDesignByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	attrs := map[string]any{}
	if len(d.Attributes) > 0 {
		_ = json.Unmarshal(d.Attributes, &attrs)
	}
	if v := trimMax(req.Description, maxProductDescription); v != "" {
		attrs["product_description"] = v
	} else {
		delete(attrs, "product_description")
	}

	var out []byte
	if len(attrs) > 0 {
		out, _ = json.Marshal(attrs)
	}
	if err := s.store.Q.SetDesignAttributes(c.Request.Context(), gen.SetDesignAttributesParams{ID: id, Attributes: out}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the description.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id.String()})
}

// replaceDesignPreview swaps the design's cover image. It reuses the create-flow
// resolver, so it accepts an uploaded "preview" file or a "preview_url" (a Shopify
// image). Guarded by design:update; needs object storage.
func (s *Server) replaceDesignPreview(c *gin.Context) {
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "The design pipeline is not configured.")
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := c.Request.ParseMultipartForm(int64(maxPreviewBytes)); err != nil {
		detail(c, http.StatusBadRequest, "Could not read the image form.")
		return
	}
	key, ok := s.resolveAndStorePreview(c, id)
	if !ok {
		return
	}
	if err := s.store.Q.SetDesignPreviewKey(c.Request.Context(), gen.SetDesignPreviewKeyParams{ID: id, PreviewKey: key}); err != nil {
		detail(c, http.StatusInternalServerError, "Stored the image but could not update the design.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id.String(), "preview_key": key})
}

// cleanStrings trims each entry and drops the blanks.
func cleanStrings(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}
