package httpapi

// Recording what a colour NAME means to the printers that have to print it.
//
// An order says "BLUE". An AMS reports "#2850E0" and nothing else - no name, no
// brand, because tray_sub_brands is empty on every tray in this fleet. Until
// this table existed, Tensor bridged the two with a built-in guess that called
// blue #1560BD, and wrote that guess into every plate's filament declaration.
// BambuBuddy compared it against the real tray and refused the plate.
//
// So the bridge becomes a thing an operator states rather than something Tensor
// infers: they look at the spool in the machine, and say "that one is our blue".
//
// A colour accepts several hexes, one of them primary. Thirteen printers do not
// agree on blue, and a single hex per name would make the exact-match check
// reject every machine holding the other one.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type colourMapEntryResponse struct {
	ID          string     `json:"id"`
	ColourName  string     `json:"colour_name"`
	Hex         string     `json:"hex"`
	IsPrimary   bool       `json:"is_primary"`
	Note        *string    `json:"note"`
	ConfirmedBy *string    `json:"confirmed_by"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toColourMapEntry(e gen.ColourMap) colourMapEntryResponse {
	return colourMapEntryResponse{
		ID: e.ID.String(), ColourName: e.ColourName, Hex: e.Hex,
		IsPrimary: e.IsPrimary, Note: e.Note, ConfirmedBy: e.ConfirmedBy,
		ConfirmedAt: db.TimePtr(e.ConfirmedAt),
		CreatedAt:   db.Time(e.CreatedAt), UpdatedAt: db.Time(e.UpdatedAt),
	}
}

// unmappedColourResponse is one spool physically loaded that Tensor cannot name.
//
// The point of the endpoint: eleven of the fourteen hexes loaded across this
// fleet are unknown to Tensor AND to BambuBuddy's own catalogue, because the
// spools are generic. Listing them with a suggested name turns filling the map
// into a few clicks instead of an operator typing hex codes.
type unmappedColourResponse struct {
	Hex string `json:"hex"`
	// Machines names every printer holding this hex, so the operator knows
	// which spool to walk over and look at.
	Machines []string `json:"machines"`
	// NearestKnownName is a suggestion only, from the closest colour Tensor
	// already knows. Never written without confirmation: a suggestion that
	// wrote itself would be the same guess this table exists to replace.
	NearestKnownName string `json:"nearest_known_name"`
}

func (s *Server) registerColourMap(g *gin.RouterGroup) {
	g.GET("/colour-map", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listColourMap)
	// Unmapped is a read of the fleet, so it needs no manage permission - but it
	// is the page an operator opens BEFORE confirming anything.
	g.GET("/colour-map/unmapped", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listUnmappedColours)
	g.POST("/colour-map", s.guards.RequirePermission(auth.FilamentManage.Key()), s.upsertColourMap)
	g.PATCH("/colour-map/:id", s.guards.RequirePermission(auth.FilamentManage.Key()), s.patchColourMap)
	g.DELETE("/colour-map/:id", s.guards.RequirePermission(auth.FilamentManage.Key()), s.deleteColourMap)
}

func (s *Server) listColourMap(c *gin.Context) {
	rows, err := s.store.Q.ListColourMap(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the colour map.")
		return
	}
	out := make([]colourMapEntryResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toColourMapEntry(r))
	}
	c.JSON(http.StatusOK, out)
}

type colourMapUpsertRequest struct {
	ColourName string  `json:"colour_name" binding:"required"`
	Hex        string  `json:"hex" binding:"required"`
	IsPrimary  bool    `json:"is_primary"`
	Note       *string `json:"note"`
}

func (s *Server) upsertColourMap(c *gin.Context) {
	var req colourMapUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "A colour name and a hex are both required.")
		return
	}
	// Canonicalised on the way in, so SKY BLUE and BLUE share one spool's
	// mapping rather than quietly becoming two colours that disagree.
	name := production.CanonicalColourName(req.ColourName)
	if name == "" {
		detail(c, http.StatusUnprocessableEntity, "That is not a colour name.")
		return
	}
	hex, ok := normaliseHex(req.Hex)
	if !ok {
		detail(c, http.StatusUnprocessableEntity,
			"That is not a colour value. Use the hex the printer reports, e.g. #2850E0.")
		return
	}

	ctx := c.Request.Context()
	entry, err := s.store.Q.UpsertColourMapEntry(ctx, gen.UpsertColourMapEntryParams{
		ID: uuid.New(), ColourName: name, Hex: hex,
		// First entry for a colour becomes its primary automatically: a map
		// with no primary resolves to nothing, which would look exactly like
		// not having added it at all.
		IsPrimary:   req.IsPrimary || !s.colourHasPrimary(ctx, name),
		Note:        req.Note,
		ConfirmedBy: nonEmptyPtr(currentUserID(c)),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record that colour.")
		return
	}

	// Promotion is a second statement because the upsert cannot know whether it
	// created or updated the row it is promoting.
	if entry.IsPrimary {
		if err := s.store.Q.SetColourMapPrimary(ctx, entry.ID); err != nil {
			detail(c, http.StatusInternalServerError, "Could not make that the primary colour.")
			return
		}
		entry.IsPrimary = true
	}
	c.JSON(http.StatusCreated, toColourMapEntry(entry))
}

// colourHasPrimary reports whether this colour already resolves to something.
func (s *Server) colourHasPrimary(ctx context.Context, name string) bool {
	hex, err := s.store.Q.GetColourMapPrimaryHex(ctx, name)
	return err == nil && strings.TrimSpace(hex) != ""
}

type colourMapPatchRequest struct {
	Hex       *string `json:"hex"`
	Note      *string `json:"note"`
	IsPrimary *bool   `json:"is_primary"`
}

func (s *Server) patchColourMap(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req colourMapPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Nothing to change.")
		return
	}
	ctx := c.Request.Context()

	var hex *string
	if req.Hex != nil {
		normalised, ok := normaliseHex(*req.Hex)
		if !ok {
			detail(c, http.StatusUnprocessableEntity,
				"That is not a colour value. Use the hex the printer reports, e.g. #2850E0.")
			return
		}
		hex = &normalised
	}

	entry, err := s.store.Q.UpdateColourMapEntry(ctx, gen.UpdateColourMapEntryParams{
		ID: id, Hex: hex, Note: req.Note,
		ConfirmedBy: nonEmptyPtr(currentUserID(c)),
	})
	if err != nil {
		dbError(c, err, "That colour mapping does not exist.", "Could not update that colour.")
		return
	}

	if req.IsPrimary != nil && *req.IsPrimary {
		if err := s.store.Q.SetColourMapPrimary(ctx, entry.ID); err != nil {
			detail(c, http.StatusInternalServerError, "Could not make that the primary colour.")
			return
		}
		entry.IsPrimary = true
	}
	c.JSON(http.StatusOK, toColourMapEntry(entry))
}

func (s *Server) deleteColourMap(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.DeleteColourMapEntry(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete that colour.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "That colour mapping does not exist.")
		return
	}
	c.Status(http.StatusNoContent)
}

// listUnmappedColours reports every spool loaded in the fleet that Tensor cannot
// name, newest problem first: the colours held by the most machines.
func (s *Server) listUnmappedColours(c *gin.Context) {
	ctx := c.Request.Context()

	machines, err := s.store.Q.ListFleetMachines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the fleet.")
		return
	}
	mapped, err := s.store.Q.ListColourMap(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the colour map.")
		return
	}

	known := make(map[string]bool, len(mapped))
	// Candidate names for the suggestion come from what is already mapped and
	// from the built-in table, so the first spool of a colour still gets a
	// sensible guess before anything is recorded.
	candidates := make(map[string]string, len(mapped)+len(fallbackColours))
	for _, e := range mapped {
		if hex, ok := normaliseHex(e.Hex); ok {
			known[hex] = true
			candidates[e.ColourName] = hex
		}
	}
	for name, hex := range fallbackColours {
		if _, taken := candidates[strings.ToUpper(name)]; !taken {
			candidates[strings.ToUpper(name)] = hex
		}
	}

	holders := map[string][]string{}
	for _, m := range machines {
		for _, hex := range loadedColours(m) {
			if known[hex] {
				continue
			}
			holders[hex] = append(holders[hex], m.Name)
		}
	}

	out := make([]unmappedColourResponse, 0, len(holders))
	for hex, names := range holders {
		sort.Strings(names)
		out = append(out, unmappedColourResponse{
			Hex: hex, Machines: names,
			NearestKnownName: nearestColourName(hex, candidates),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Machines) != len(out[j].Machines) {
			return len(out[i].Machines) > len(out[j].Machines)
		}
		return out[i].Hex < out[j].Hex
	})
	c.JSON(http.StatusOK, out)
}

// nearestColourName is the closest colour Tensor already knows, or "" when it
// knows none.
//
// Nearest is right HERE and wrong at send time, and the difference is worth
// stating: this only pre-fills a field an operator is about to confirm while
// looking at the spool, so a poor suggestion costs one correction. The check
// that decides what actually prints stays exact - see missingColours.
func nearestColourName(hex string, candidates map[string]string) string {
	best, bestDistance := "", 0
	for name, candidate := range candidates {
		d, ok := nearestColourDistance(hex, []string{candidate})
		if !ok {
			continue
		}
		if best == "" || d < bestDistance {
			best, bestDistance = name, d
		}
	}
	return best
}
