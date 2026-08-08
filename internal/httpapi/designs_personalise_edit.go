package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// personalisationSpec is the applied personalisation on a design: the customer's
// name plus how it sits on the model and its intended colour. It is what the editor
// saves, what re-slicing bakes into the model, and what the design detail returns
// so the editor can rehydrate. Colour is stored intent (the emboss is real
// geometry; true multi-material print is a follow-up).
type personalisationSpec struct {
	Text        string  `json:"text"`
	Font        string  `json:"font"`
	Style       string  `json:"font_style"`
	SizeMM      float64 `json:"size_mm"`
	DepthMM     float64 `json:"depth_mm"`
	OffsetXMM   float64 `json:"offset_x_mm"`
	OffsetYMM   float64 `json:"offset_y_mm"`
	RotationDeg float64 `json:"rotation_deg"`
	Colour      string  `json:"colour"`
}

const (
	maxPersonaliseTextLen = 48
	minPersonaliseSizeMM  = 3
	maxPersonaliseSizeMM  = 40
	minPersonaliseDepthMM = 0.2
	maxPersonaliseDepthMM = 5
	maxPersonaliseOffset  = 500
)

var hexColourPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// normalise clamps every field to sane bounds and returns the cleaned spec. It
// does not decide whether the text is present - the caller does that.
func (p personalisationSpec) normalise() personalisationSpec {
	out := p
	out.Text = strings.TrimSpace(p.Text)
	out.Font = strings.TrimSpace(p.Font)
	out.Style = strings.TrimSpace(p.Style)
	out.SizeMM = clampFloat(p.SizeMM, minPersonaliseSizeMM, maxPersonaliseSizeMM)
	if out.DepthMM == 0 {
		out.DepthMM = 1
	}
	out.DepthMM = clampFloat(out.DepthMM, minPersonaliseDepthMM, maxPersonaliseDepthMM)
	out.OffsetXMM = clampFloat(p.OffsetXMM, -maxPersonaliseOffset, maxPersonaliseOffset)
	out.OffsetYMM = clampFloat(p.OffsetYMM, -maxPersonaliseOffset, maxPersonaliseOffset)
	out.RotationDeg = normaliseAngle(p.RotationDeg)
	out.Colour = strings.TrimSpace(p.Colour)
	return out
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// normaliseAngle folds any angle into [0, 360).
func normaliseAngle(deg float64) float64 {
	d := deg
	for d < 0 {
		d += 360
	}
	for d >= 360 {
		d -= 360
	}
	return d
}

// downloadPersonaliseText streams JUST the extruded name as its own STL, so the
// browser can render it as a separate, movable object on top of the model (the
// live editor). The base model is fetched separately. Any failure answers 404 so
// the editor can fall back to no text rather than break.
func (s *Server) downloadPersonaliseText(c *gin.Context) {
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "The design pipeline is not configured.")
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	spec := personalise.Spec{
		Text:    strings.TrimSpace(c.Query("text")),
		Font:    c.Query("font"),
		Style:   c.Query("font_style"),
		SizeMM:  queryFloat(c, "size_mm", 10),
		DepthMM: queryFloat(c, "depth_mm", 1),
	}
	if spec.Text == "" {
		detail(c, http.StatusUnprocessableEntity, "A 'text' query parameter is required.")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), personaliseTimeout)
	defer cancel()

	key, err := s.ensureTextModel(ctx, id, spec)
	if err != nil {
		s.logger.Info("personalise text render failed", "design", id, "reason", err)
		detail(c, http.StatusNotFound, "Could not render the text.")
		return
	}
	s.streamObject(c, key, "personalise-text.stl")
}

// ensureTextModel renders the extruded text STL once and caches it under a key
// derived from the design and the exact text spec. Returns the cached object key.
func (s *Server) ensureTextModel(ctx context.Context, id uuid.UUID, spec personalise.Spec) (string, error) {
	key := textModelKey(id, spec)
	if obj, err := s.storage.Get(ctx, key); err == nil {
		_ = obj.Body.Close()
		return key, nil
	}
	stl, err := personalise.GenerateTextSTL(ctx, s.cfg.OpenSCADBin, spec)
	if err != nil {
		return "", err
	}
	return key, s.storage.Put(ctx, key, strings.NewReader(string(stl)), int64(len(stl)), "model/stl")
}

// textModelKey is the deterministic cache key for one design's extruded text mesh,
// derived from the exact text spec so a repeated request hits the cache.
func textModelKey(id uuid.UUID, spec personalise.Spec) string {
	sig := fmt.Sprintf("%s|%s|%s|%.3f|%.3f", strings.TrimSpace(spec.Text), spec.Font, spec.Style, spec.SizeMM, spec.DepthMM)
	sum := sha256.Sum256([]byte(sig))
	return fmt.Sprintf("designs/%s/text-%s.stl", id, hex.EncodeToString(sum[:8]))
}

// setDesignPersonalisation saves the applied personalisation, bakes the merged
// (base + text) STL, and re-slices from it so cost and G-code include the name. A
// request with empty text clears personalisation and re-slices the plain model.
func (s *Server) setDesignPersonalisation(c *gin.Context) {
	if !s.pipelineReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var spec personalisationSpec
	if !bindJSON(c, &spec) {
		return
	}
	spec = spec.normalise()
	if spec.Colour != "" && !hexColourPattern.MatchString(spec.Colour) {
		detail(c, http.StatusUnprocessableEntity, "Colour must be a #rrggbb hex value.")
		return
	}
	if len([]rune(spec.Text)) > maxPersonaliseTextLen {
		detail(c, http.StatusUnprocessableEntity, "The name is too long.")
		return
	}

	ctx := c.Request.Context()
	d, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	sliceKey, personalisationJSON, ok := s.bakePersonalisation(c, d, spec)
	if !ok {
		return
	}

	row, err := s.store.Q.SetDesignPersonalisation(ctx, gen.SetDesignPersonalisationParams{
		ID: id, Personalisation: personalisationJSON, PersonalisedStlKey: nullableString(sliceKey, d.StlKey),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the personalisation.")
		return
	}

	// Re-slice from the chosen STL (baked model when text is present, the plain
	// model when cleared) using the design's current settings, so cost and layers
	// reflect what was saved.
	form := designFormFromRow(d)
	if !s.applyResubmit(c, id, sliceKey, form) {
		return
	}

	c.JSON(http.StatusOK, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct,
		row.PreviewKey, row.Sku, row.CreatedAt, row.UpdatedAt))
}

// bakePersonalisation resolves the STL to slice and the personalisation JSON to
// store. Empty text clears personalisation (slice the plain model, store NULL);
// otherwise it bakes the merged STL and encodes the spec. Returns ok=false after
// writing an error response.
func (s *Server) bakePersonalisation(
	c *gin.Context, d gen.GetDesignByIDRow, spec personalisationSpec,
) (sliceKey string, personalisationJSON []byte, ok bool) {
	if spec.Text == "" {
		return d.StlKey, nil, true
	}
	if strings.ToLower(filepath.Ext(d.StlKey)) != ".stl" {
		detail(c, http.StatusUnprocessableEntity, "Only STL models can be personalised.")
		return "", nil, false
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), personaliseTimeout)
	defer cancel()

	pKey, err := s.ensurePersonalisedModel(ctx, d.StlKey, personalise.Spec{
		Text: spec.Text, Font: spec.Font, Style: spec.Style, SizeMM: spec.SizeMM, DepthMM: spec.DepthMM,
	}, textPlacement{OffsetXMM: spec.OffsetXMM, OffsetYMM: spec.OffsetYMM, RotationDeg: spec.RotationDeg})
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Could not render the personalised model. Check the name and try again.")
		return "", nil, false
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the personalisation.")
		return "", nil, false
	}
	return pKey, encoded, true
}

// designFormFromRow rebuilds the slice inputs from a design's stored answers, for
// a re-slice that changes only the model file (personalisation), not the settings.
func designFormFromRow(d gen.GetDesignByIDRow) designForm {
	form := designForm{
		Brand: d.BrandSlug, Material: d.Material, Finish: d.Finish, Quality: d.Quality,
		UnitsPerBed: d.UnitsPerBed, InfillPct: d.InfillPct, Colour: d.Colour,
		MachineID: d.MachineID, Settings: slicing.SliceSettings{}.Normalize(),
	}
	return form
}

// nullableString returns a pointer to v, or nil when v equals the sentinel (used to
// store NULL for personalised_stl_key when the slice key is just the plain model).
func nullableString(v, sentinel string) *string {
	if v == sentinel {
		return nil
	}
	return &v
}
