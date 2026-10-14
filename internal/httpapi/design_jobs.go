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
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
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
		// Field-for-field identical to gen.ProductionJob, as every list row
		// is - see asProductionJobs for why sqlc makes a separate type and
		// why the conversion is free.
		mine = append(mine, gen.ProductionJob(j))
	}

	out := s.productionJobsDTO(ctx, mine)
	// What the customer changed, computed in the same pass. The query carries
	// the two jsonb columns in full for a stale row precisely so this needs no
	// second request per job - and it is the question the designer opened the
	// page to answer, so it should not be a click away.
	for i := range out {
		out[i].PersonalisationChanges = personalisationChangesFor(mine[i])
	}
	c.JSON(http.StatusOK, out)
}
