package httpapi

// The designer's half of the production queue.
//
// A product Tensor cannot render needs a 3MF from a person before it can
// print. That work has always existed and there has never been a screen for
// it: the jobs sat in the production queue under "Design file required",
// behind production:read, which a DESIGNER does not hold and should not - see
// the note on DesignJobRead in internal/auth/catalog.go.
//
// So this is a narrow group rather than a widened role. It serves exactly two
// things: the jobs waiting for a file, and the upload that supplies one.
//
// ITS OWN PATH PREFIX, not /designs/..., and that is forced rather than
// chosen. designs.go already registers GET /designs/:id, and Gin panics at
// startup when a static segment and a wildcard share a position - the same
// constraint orders.go documents.

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func (s *Server) registerDesignJobRoutes(r *gin.Engine) {
	g := r.Group("/design-jobs")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.DesignJobRead.Key()), s.listDesignJobs)
	g.POST("/:id/model",
		s.guards.RequirePermission(auth.DesignJobSupply.Key()), s.supplyDesignJobModel)
}

// listDesignJobs is the queue a designer works down.
//
// Only jobs whose customer's window has closed (the SQL says why), only the
// products Tensor does not render, and only the two states that are work:
// there is no file, or the customer changed the order after the file was made.
// A job leaves the list by being given a file, so the list emptying is the
// signal that there is nothing to do.
func (s *Server) listDesignJobs(c *gin.Context) {
	ctx := c.Request.Context()

	rows, err := s.store.Q.ListJobsNeedingDesignFile(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design queue.")
		return
	}

	// The non-DNP rule stays in Go, where IsGeneratedProduct is, rather than
	// being restated in SQL where the two could drift. Same split
	// orderIDsWithNonGeneratedItems uses.
	mine := make([]gen.ProductionJob, 0, len(rows))
	for _, j := range rows {
		if IsGeneratedProduct(deref(j.Sku), deref(j.ProductName)) {
			continue
		}
		if isChargeLine(deref(j.ProductName)) {
			continue
		}
		// Field-for-field identical to gen.ProductionJob, as every list row
		// is - see asProductionJobs for why sqlc makes a separate type and
		// why the conversion is free.
		mine = append(mine, gen.ProductionJob(j))
	}

	out := s.productionJobsDTO(ctx, mine)
	// What the customer changed, computed in the same pass. The query carries
	// the two jsonb columns in full precisely so this needs no second request
	// per job - and it is the question the designer opened the page to answer,
	// so it should not be a click away.
	for i := range out {
		out[i].PersonalisationChanges = personalisationChangesFor(mine[i])
	}

	// The filaments the shop can print, sent WITH the jobs rather than from a
	// route of their own.
	//
	// A designer needs them to say what a colour in their file is - a slicer
	// writes its own hex, and #C12E1F is Bambu Basic Red rather than this
	// shop's RED. The existing colour-map endpoint is behind filament:read,
	// which a DESIGNER does not hold, and a second route under /design-jobs
	// would collide with /:id the way /designs/... already does.
	c.JSON(http.StatusOK, designJobsResponse{
		Jobs:    out,
		Colours: s.printableColourNames(ctx),
	})
}

// designJobsResponse is the queue and what a designer needs to act on it.
type designJobsResponse struct {
	Jobs    []productionJobResponse `json:"jobs"`
	Colours []string                `json:"colours"`
}

// printableColourNames is every filament name this shop can resolve, in one
// list for a picker.
//
// The confirmed spools and the built-in table together, because either alone
// is a narrower answer than the renderer would give: resolveColourHex consults
// both, so a name from either is one that can actually be printed.
func (s *Server) printableColourNames(ctx context.Context) []string {
	seen := map[string]bool{}
	for name := range fallbackColours {
		seen[production.CanonicalColourName(name)] = true
	}
	if rows, err := s.store.Q.ListColourMap(ctx); err == nil {
		for _, r := range rows {
			if name := production.CanonicalColourName(r.ColourName); name != "" {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// chargeLineNames are storefront lines that are money rather than an object.
//
// A customer pays a customisation charge; nobody draws one. The line still
// becomes a production job, because job creation makes one per line and has no
// way to tell - so it arrives in the design queue asking a designer to model a
// fee, and sits there for ever because there is nothing they could upload.
//
// Matched as a substring of the lower-cased product name, the same way
// generatedProductNames is, because the storefront appends and reorders words
// around its own names freely. A short explicit list rather than a rule: "has
// no SKU and no customisation" would also describe a real product somebody
// forgot to configure, and hiding that from the one screen that would surface
// it is the opposite of helpful.
var chargeLineNames = []string{
	"customization charge",
	"customisation charge",
}

// isChargeLine reports whether a line is a fee rather than something to make.
func isChargeLine(productName string) bool {
	name := strings.ToLower(strings.TrimSpace(productName))
	if name == "" {
		return false
	}
	for _, known := range chargeLineNames {
		if strings.Contains(name, known) {
			return true
		}
	}
	return false
}
