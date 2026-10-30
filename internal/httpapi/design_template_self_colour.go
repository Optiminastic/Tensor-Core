package httpapi

// A design file that states its own colours.
//
// Colour has always been configured IN TENSOR - a scheme per variant naming
// each piece a file draws and the filament it prints in. That fits a product
// whose colourways are one geometry differing by filament, and fits the Diwali
// products badly: the lotus holder is three colourways whose back, outline,
// text and cup are three different palettes, which was nine rows typed by hand.
//
// A flagged file is one whose pieces and colours are READ FROM THE FILE - its
// own `<PIECE>_COLOR = "..."` declarations, which personalise.PartColours has
// parsed since the preview needed them. Nothing about colour is configured for
// a SKU printing from it.
//
// IT STILL RENDERS ONE PASS PER PIECE, and that is the thing to hold on to.
// OpenSCAD's STL output carries no colour and meshio.Write3MF refuses a part
// without a hex, so colour reaches the 3MF only because each piece is rendered
// separately and written with its own. Rendering a flagged file once, as one
// object, would come out a single colour - the lotus losing its gold text and
// its pink outline. "The file holds the colours" means Tensor reads them.

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

type selfColourRequest struct {
	// SelfColoured false clears the flag, so a file can stop being one.
	SelfColoured *bool `json:"self_coloured"`
}

func (s *Server) setTemplateSelfColour(c *gin.Context) {
	ctx := c.Request.Context()
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		detail(c, http.StatusUnprocessableEntity, "Name the design file.")
		return
	}
	var req selfColourRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SelfColoured == nil {
		detail(c, http.StatusUnprocessableEntity,
			"Say whether this file states its own colours: true or false.")
		return
	}

	if !*req.SelfColoured {
		// CLEARING IT IS THE RISKY DIRECTION. The very next render starts
		// asking this file for the pieces a scheme names, and a file written
		// to paint itself may name them differently or not branch at all -
		// which renders an empty piece and holds a paid order. So it is
		// refused unless somebody has configured pieces for it to answer.
		if why, blocked := s.clearingSelfColourStrands(ctx, key); blocked {
			detail(c, http.StatusUnprocessableEntity, why)
			return
		}
		if err := s.store.Q.ClearSelfColouredTemplate(ctx, key); err != nil {
			detail(c, http.StatusInternalServerError, "Could not change that design file.")
			return
		}
		c.Status(http.StatusNoContent)
		return
	}

	// SETTING IT IS CHECKED, because a file that declares no colours would
	// silently fall back to the configured scheme and the flag would look
	// like it had done nothing.
	if declared := s.declaredPartColours(ctx, key); len(declared) == 0 {
		detail(c, http.StatusUnprocessableEntity,
			key+" declares no colours, so there is nothing to read. A file states its "+
				`own colours with a top-level declaration per piece - BACK_COLOR = "#7D3CB5"; `+
				"- naming the same pieces it branches on PART for.")
		return
	}

	if err := s.store.Q.SetSelfColouredTemplate(ctx, gen.SetSelfColouredTemplateParams{
		TemplateKey: key, UpdatedBy: currentUserID(c),
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not change that design file.")
		return
	}
	c.Status(http.StatusNoContent)
}

// clearingSelfColourStrands reports whether un-flagging this file would leave
// something printing from it with no pieces to ask for.
func (s *Server) clearingSelfColourStrands(ctx context.Context, key string) (string, bool) {
	rows, err := s.store.Q.ListProductsUsingTemplate(ctx, key)
	if err != nil || len(rows) == 0 {
		// Nothing renders from it, so nothing can break. An unreadable
		// registry allows the change rather than refusing on a guess.
		return "", false
	}
	for _, r := range rows {
		product, err := s.store.Q.GetProductByCode(ctx, r.Code)
		if err != nil {
			continue
		}
		parts, err := s.store.Q.ListDesignColourParts(ctx, gen.ListDesignColourPartsParams{
			ProductID: product.ID, Role: r.Role,
		})
		if err == nil && len(parts) > 0 {
			return "", false
		}
	}
	return key + " still renders " + rows[0].Code +
		", and nothing says which pieces it draws. Configure its colour scheme first, " +
		"or the next order for it renders an empty model.", true
}

// declaredPartColours is the pieces a file paints itself, AS WRITTEN, by
// lower-cased name. A value may be "#7D3CB5" or "purple".
//
// Read through the renderer's Source, like templateParams, so it is the file a
// render would ACTUALLY use - the uploaded override where there is one, the
// embedded copy otherwise. Empty on any failure: a file that cannot be read
// must not be flagged on a guess.
func (s *Server) declaredPartColours(ctx context.Context, key string) map[string]string {
	if s.renderer == nil {
		return nil
	}
	source, err := s.renderer.Source(ctx, key)
	if err != nil || len(source) == 0 {
		return nil
	}
	return personalise.PartColourValues(source)
}

// selfColouredParts is a flagged file's scheme, read out of the file.
//
// Returns nil when the file is not flagged, cannot be read, or declares no
// colours - in every case the caller carries on to the configured scheme, so
// this can only ever ADD a way to be configured, never take one away. That
// discipline is colourPartsForJob's own, stated where it falls back.
//
// Ordered by piece name so a model's objects land in the same order every
// render. Unordered, a re-render would reshuffle the slicer's object list and
// an operator comparing two plates would see them differently arranged.
func (s *Server) selfColouredParts(ctx context.Context, key string) []colourPart {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	self, err := s.store.Q.IsSelfColouredTemplate(ctx, key)
	if err != nil || !self {
		return nil
	}
	declared := s.declaredPartColours(ctx, key)
	if len(declared) == 0 {
		return nil
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]colourPart, 0, len(names))
	for _, name := range names {
		hex, ok := s.declaredColourHex(ctx, declared[name])
		if !ok {
			// ALL OR NOTHING. A scheme missing one piece renders a model
			// missing that piece, which slices and prints and is wrong. Better
			// to fall through to the configured scheme, which is a set of
			// colours somebody chose, than to print most of a product.
			return nil
		}
		out = append(out, colourPart{Name: name, Hex: hex})
	}
	return out
}

// declaredColourHex turns a declared value into a hex this shop can print.
//
// A hex is taken as written. A NAME is resolved against the filament shelf,
// exactly as a name typed into the colour scheme is - the shop writes
// BACK_COLOR = "purple" and means its purple spool, and the catalogue's idea
// of purple is not necessarily the one on the shelf. dnp_colour.go records
// what happens when the two differ: the catalogue's blue against a shelf
// holding another made BambuBuddy refuse plate after plate.
func (s *Server) declaredColourHex(ctx context.Context, written string) (string, bool) {
	v := strings.TrimSpace(written)
	if v == "" {
		return "", false
	}
	if strings.HasPrefix(v, "#") {
		if len(v) != 7 {
			return "", false
		}
		return strings.ToUpper(v), true
	}
	resolved, err := s.resolveColourHex(ctx, v)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// selfColouredKeys is every flagged file, by lower-cased key.
//
// One read for the whole Designs tab, beside templateSectionsByKey and for the
// same reason. An unreadable table answers "none", which renders every file as
// configured-in-Tensor - the state before the flag existed.
func (s *Server) selfColouredKeys(c *gin.Context) map[string]bool {
	rows, err := s.store.Q.ListSelfColouredTemplates(c.Request.Context())
	if err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(rows))
	for _, key := range rows {
		out[strings.ToLower(key)] = true
	}
	return out
}
