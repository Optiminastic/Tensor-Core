package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// costReportRow is one row of a brand's Cost Reports table: a priced design with
// its Design CP, recommended SP, CP% and Green/Yellow/Red verdict. Its JSON shape
// is the contract the frontend validates.
type costReportRow struct {
	ID            string    `json:"id"`
	BrandSlug     string    `json:"brand_slug"`
	Name          string    `json:"name"`
	SKU           *string   `json:"sku"`
	Status        string    `json:"status"`
	DesignCP      float64   `json:"design_cp"`
	Verdict       string    `json:"verdict"`
	CPPct         float64   `json:"cp_pct"`
	RecommendedSP *int      `json:"recommended_sp"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// registerCostReport wires the brand's Cost Reports endpoint: every priced design
// with its cost, price and verdict. Guarded by pricing:read + brand access.
func (s *Server) registerCostReport(r *gin.Engine) {
	r.GET("/brands/:slug/cost-report",
		s.guards.RequireUser(),
		s.guards.RequirePermission(auth.PricingRead.Key()),
		s.listDesignCostReport)
}

// listDesignCostReport returns the brand's priced designs with their costing,
// newest first. Guarded by pricing:read + brand access.
func (s *Server) listDesignCostReport(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.requireBrandAccess(c, slug) {
		return
	}
	rows, err := s.store.Q.ListDesignCostReport(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the cost report.")
		return
	}
	out := make([]costReportRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, costReportRowDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

func costReportRowDTO(r gen.ListDesignCostReportRow) costReportRow {
	var sp *int
	if r.RecommendedSp != nil {
		v := int(*r.RecommendedSp)
		sp = &v
	}
	return costReportRow{
		ID:            r.ID.String(),
		BrandSlug:     r.BrandSlug,
		Name:          r.Name,
		SKU:           r.Sku,
		Status:        r.Status,
		DesignCP:      r.DesignCp,
		Verdict:       r.Verdict,
		CPPct:         r.CpPct,
		RecommendedSP: sp,
		UpdatedAt:     db.Time(r.UpdatedAt),
	}
}
