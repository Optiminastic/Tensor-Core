package httpapi

// POST /filament-inventory/sync - mirror BambuBuddy's spool shelf into Tensor's
// filament inventory.
//
// The two systems count filament at different granularities. BambuBuddy tracks
// individual spools; Tensor tracks availability per material and colour, which
// is the only question the batch planner asks. So this aggregates: every spool
// of the same material and colour is summed into one bucket.
//
// BambuBuddy wins on the numbers. It is where an operator scans, weighs and
// retires physical spools, so it is the honest source of truth for what is
// actually on the shelf. The cost is real and worth stating plainly: any
// Tensor-side debit not yet reflected in a spool's weight_used is overwritten
// on the next sync.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// SyncFilamentResult reports what one sync changed.
type SyncFilamentResult struct {
	// Spools counted, before aggregation - what the operator sees in
	// BambuBuddy, so the two numbers can be compared at a glance.
	Spools int `json:"spools"`
	// Created and Updated are material/colour buckets, not spools.
	Created int `json:"created"`
	Updated int `json:"updated"`
	// TotalGrams is the whole shelf, so "129.0kg" can be checked against
	// BambuBuddy's own total without opening it.
	TotalGrams float64 `json:"total_grams"`
}

// filamentBucket is one material/colour combination and its summed stock.
type filamentBucket struct {
	material string
	colour   string
	grams    float64
}

// SyncFilamentFromBambuBuddy replaces Tensor's stock figures with BambuBuddy's.
func (s *Server) SyncFilamentFromBambuBuddy(ctx context.Context) (SyncFilamentResult, error) {
	log := obs.FromContext(ctx)
	if !s.bambu.Configured() {
		return SyncFilamentResult{}, fmt.Errorf(
			"BambuBuddy is not configured (set BAMBUBUDDY_URL and BAMBUBUDDY_API_KEY)")
	}

	spools, err := s.bambu.ListSpools(ctx)
	if err != nil {
		return SyncFilamentResult{}, fmt.Errorf("list spools: %w", err)
	}

	buckets := aggregateSpools(spools)
	out := SyncFilamentResult{Spools: len(spools)}

	for _, b := range buckets {
		out.TotalGrams += b.grams

		created, err := s.applyFilamentBucket(ctx, b)
		if err != nil {
			// One material failing must not abandon the rest: a sync that
			// stops halfway leaves the shelf half-mirrored, which is harder to
			// reason about than one material being wrong.
			log.Warn("could not sync a filament bucket",
				"material", b.material, "colour", b.colour, "error", err)
			continue
		}
		if created {
			out.Created++
		} else {
			out.Updated++
		}
	}

	log.Info("filament synced from bambubuddy",
		"spools", out.Spools, "created", out.Created, "updated", out.Updated,
		"total_grams", out.TotalGrams)
	return out, nil
}

// aggregateSpools sums spools into (material, colour) buckets.
//
// Archived spools are excluded: BambuBuddy archives a spool when it is used up
// or retired, and counting those would report filament that is not on the shelf.
func aggregateSpools(spools []bambubuddy.Spool) []filamentBucket {
	totals := make(map[string]*filamentBucket, len(spools))
	for _, sp := range spools {
		if sp.Archived {
			continue
		}
		material := strings.TrimSpace(sp.Material)
		if material == "" {
			continue // nothing to key on; a spool with no material is unusable
		}
		colour := sp.Colour()
		key := material + "\x00" + colour
		if existing, ok := totals[key]; ok {
			existing.grams += sp.RemainingGrams()
			continue
		}
		totals[key] = &filamentBucket{material: material, colour: colour, grams: sp.RemainingGrams()}
	}

	buckets := make([]filamentBucket, 0, len(totals))
	for _, b := range totals {
		buckets = append(buckets, *b)
	}
	return buckets
}

// applyFilamentBucket writes one bucket, creating the row if it is new.
// Reports whether it created rather than updated.
func (s *Server) applyFilamentBucket(ctx context.Context, b filamentBucket) (bool, error) {
	var colour *string
	if b.colour != "" {
		colour = &b.colour
	}

	existing, err := s.store.Q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
		Material: b.material, Colour: colour,
	})
	if err != nil {
		if !isNoRows(err) {
			return false, err
		}
		_, err = s.store.Q.InsertFilament(ctx, gen.InsertFilamentParams{
			ID: uuid.New(), Material: b.material, Colour: colour,
			GramsAvailable: b.grams,
			// A new row gets no reorder level. Inventing one would put a
			// material straight into "low stock" on the strength of a guess;
			// the threshold is an operator's judgement about lead time.
			ReorderLevelGrams: 0,
		})
		if err != nil {
			return false, err
		}
		return true, nil
	}

	// The reorder level is Tensor's own setting and BambuBuddy knows nothing
	// about it, so it is carried through untouched rather than reset.
	_, err = s.store.Q.UpdateFilamentLevel(ctx, gen.UpdateFilamentLevelParams{
		ID: existing.ID, GramsAvailable: b.grams,
		ReorderLevelGrams: db.NumFloat(existing.ReorderLevelGrams),
	})
	return false, err
}

func (s *Server) syncFilamentInventory(c *gin.Context) {
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	result, err := s.SyncFilamentFromBambuBuddy(c.Request.Context())
	if err != nil {
		obs.FromContext(c.Request.Context()).Error("filament sync failed", "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusBadGateway, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not reach BambuBuddy to sync filament.")
		return
	}
	c.JSON(http.StatusOK, result)
}
