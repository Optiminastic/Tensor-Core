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
	"github.com/Optiminastic/tensor-core/internal/obs"
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

// loadedColourResponse is one spool physically loaded across the fleet, and
// what the shop calls it.
//
// Every loaded spool, not only the unnamed ones. Listing just the unnamed made
// the panel empty itself as it was filled, which was satisfying and wrong: a
// spool named GOLD in haste is then invisible, and the one thing an operator
// needs to do about it - call it something else - has nowhere to happen.
//
// The names matter more than they look. Eleven of the fourteen hexes loaded
// here are unknown to Tensor AND to BambuBuddy's own catalogue, because the
// spools are generic; nothing but a person can say which one is the shop's
// gold.
type loadedColourResponse struct {
	Hex string `json:"hex"`
	// Machines names every printer holding this hex, so the operator knows
	// which spool to walk over and look at.
	Machines []string `json:"machines"`
	// MappedAs is what the shop calls this spool, empty when nobody has said.
	MappedAs string `json:"mapped_as"`
	// EntryID is the colour_map row recording it, so the panel can correct or
	// remove the mapping without going looking for it.
	EntryID string `json:"entry_id"`
	// IsPrimary reports that this spool is the one models of that colour are
	// rendered and sliced in, rather than an accepted alternative.
	IsPrimary bool `json:"is_primary"`
	// CatalogueName is what the filament's MANUFACTURER calls this exact hex,
	// from BambuBuddy's shipped colour list - "Latte Brown" for #D3B7A7. Empty
	// for a generic spool nobody registered, which is most of this fleet.
	//
	// The best suggestion available, because it is a real name for this exact
	// colour rather than a guess at which known colour it resembles. Still only
	// a suggestion: it says what a manufacturer calls it, not what this shop
	// does, and a shop that has always called the brown-ish spool GOLD is not
	// wrong.
	CatalogueName string `json:"catalogue_name"`
	// CatalogueBrand is who calls it that, so the suggestion can be judged.
	CatalogueBrand string `json:"catalogue_brand"`
	// NearestKnownName is the weaker fallback, from the closest colour Tensor
	// already knows, and only offered for a spool nobody has named. Never
	// written without confirmation: a suggestion that wrote itself would be the
	// same guess this table exists to replace.
	NearestKnownName string `json:"nearest_known_name"`
}

func (s *Server) registerColourMap(g *gin.RouterGroup) {
	g.GET("/colour-map", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listColourMap)
	// A read of the fleet, so it needs no manage permission - but it is the
	// page an operator opens BEFORE confirming anything.
	g.GET("/colour-map/loaded", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listLoadedColours)
	// Refreshing reads BambuBuddy and writes the fleet mirror, so it is guarded
	// as a change even though what it returns is a list.
	g.POST("/colour-map/refresh", s.guards.RequirePermission(auth.FilamentManage.Key()), s.refreshLoadedColours)
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
	// First entry for a colour becomes its primary automatically: a map with no
	// primary resolves to nothing, which would look exactly like not having
	// added it at all.
	wantPrimary := req.IsPrimary || !s.colourHasPrimary(ctx, name)

	// Inserted as an ALTERNATIVE whatever the caller asked for, then promoted
	// below. Only one row per colour may carry is_primary - a partial unique
	// index enforces it - so inserting a second primary directly is a
	// constraint violation, which is what naming a second blue spool used to
	// do: it answered 500 and recorded nothing.
	entry, err := s.store.Q.UpsertColourMapEntry(ctx, gen.UpsertColourMapEntryParams{
		ID: uuid.New(), ColourName: name, Hex: hex,
		IsPrimary:   false,
		Note:        req.Note,
		ConfirmedBy: nonEmptyPtr(currentUserID(c)),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record that colour.")
		return
	}

	// Promotion is a second statement, and a single UPDATE that demotes the
	// others in the same breath, so the index is never transiently violated.
	if wantPrimary {
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
	// ColourName moves this spool to a different colour - "that one is not our
	// blue, it is our teal". The spool does not change; what the shop calls it
	// does, which is the only thing this table ever recorded.
	ColourName *string `json:"colour_name"`
	Hex        *string `json:"hex"`
	Note       *string `json:"note"`
	IsPrimary  *bool   `json:"is_primary"`
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

	var name *string
	if req.ColourName != nil {
		canonical := production.CanonicalColourName(*req.ColourName)
		if canonical == "" {
			detail(c, http.StatusUnprocessableEntity, "That is not a colour name.")
			return
		}
		name = &canonical
	}

	entry, err := s.store.Q.UpdateColourMapEntry(ctx, gen.UpdateColourMapEntryParams{
		ID: id, ColourName: name, Hex: hex, Note: req.Note,
		ConfirmedBy: nonEmptyPtr(currentUserID(c)),
	})
	if err != nil {
		dbError(c, err, "That colour mapping does not exist.", "Could not update that colour.")
		return
	}

	// A spool moved to another colour cannot stay primary of the one it left -
	// the row carries the flag, not the colour - so it arrives as an
	// alternative unless its new colour has no primary yet, in which case a map
	// entry that resolves to nothing would be worse.
	promote := req.IsPrimary != nil && *req.IsPrimary
	if name != nil && !promote {
		promote = !s.colourHasPrimary(ctx, *name)
	}
	if promote {
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
func (s *Server) listLoadedColours(c *gin.Context) {
	s.respondLoadedColours(c)
}

// refreshLoadedColours asks the printers what they are holding, right now.
//
// The list is normally read from machines.filaments, which the fleet sync
// refreshes every sixty seconds. That is fine for a page somebody is reading
// and wrong for the moment they have just walked over and changed a spool:
// they come back, see the old colour, and have no way to tell whether Tensor
// is behind or the AMS did not register the swap.
//
// So this goes and asks. It runs the same sync the worker does - the mirror is
// what the queue picker ranks on, so refreshing only this page's copy would
// leave the two disagreeing - and never prunes, because a printer briefly
// unreachable is not one somebody has removed from the shop.
func (s *Server) refreshLoadedColours(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	if _, err := s.RefreshFleetFromBambuBuddy(ctx); err != nil {
		// Answer with the mirror anyway rather than nothing. A partial read is
		// still more than an empty page, and the spools that did report are
		// still nameable.
		obs.FromContext(ctx).Warn("could not refresh the fleet before listing colours", "error", err)
	}
	s.respondLoadedColours(c)
}

func (s *Server) respondLoadedColours(c *gin.Context) {
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

	// What each loaded hex is already recorded as, and the candidate names a
	// suggestion can draw on. Candidates come from what is mapped AND from the
	// built-in table, so the first spool of a colour still gets a sensible
	// guess before anything is recorded.
	recorded := make(map[string]gen.ColourMap, len(mapped))
	candidates := make(map[string]string, len(mapped)+len(fallbackColours))
	for _, e := range mapped {
		hex, ok := normaliseHex(e.Hex)
		if !ok {
			continue
		}
		// Primary wins the row: a hex may be recorded under one colour only,
		// but reading defensively costs nothing and the primary is the one the
		// slicer will use.
		if existing, seen := recorded[hex]; !seen || (e.IsPrimary && !existing.IsPrimary) {
			recorded[hex] = e
		}
		candidates[e.ColourName] = hex
	}
	for name, hex := range fallbackColours {
		if _, taken := candidates[strings.ToUpper(name)]; !taken {
			candidates[strings.ToUpper(name)] = hex
		}
	}

	// What the manufacturers call these hexes. Best-effort and cached for an
	// hour; an empty map simply means every suggestion falls back to the
	// nearest colour Tensor already knows.
	catalogue := s.colourNames.lookup(ctx, s.bambu.ListColourCatalogue)

	holders := map[string][]string{}
	for _, m := range machines {
		for _, hex := range loadedColours(m) {
			holders[hex] = append(holders[hex], m.Name)
		}
	}

	out := make([]loadedColourResponse, 0, len(holders))
	for hex, names := range holders {
		sort.Strings(names)
		row := loadedColourResponse{Hex: hex, Machines: names}
		if e, ok := recorded[hex]; ok {
			row.MappedAs = e.ColourName
			row.EntryID = e.ID.String()
			row.IsPrimary = e.IsPrimary
		} else {
			// Only offered for a spool nobody has named. Suggesting a different
			// name for one already confirmed would invite second-guessing a
			// person who walked over and looked.
			if named, ok := catalogue[hex]; ok {
				row.CatalogueName = named.Name
				row.CatalogueBrand = named.Brand
			}
			row.NearestKnownName = nearestColourName(hex, candidates)
		}
		out = append(out, row)
	}
	// Unnamed first - they are the work - then the most widely loaded, so the
	// spool in nine printers outranks the one in a single tray.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].MappedAs == "") != (out[j].MappedAs == "") {
			return out[i].MappedAs == ""
		}
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
