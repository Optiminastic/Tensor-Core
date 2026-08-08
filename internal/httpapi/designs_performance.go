package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// performanceResponse is the per-product analytics: the unit economics from the
// pricing engine plus real sales/production counts for the design's catalog SKU.
type performanceResponse struct {
	SKU            string  `json:"sku"`
	HasPricing     bool    `json:"has_pricing"`
	DesignCP       float64 `json:"design_cp"`
	SellingPrice   float64 `json:"selling_price"`
	MarginPerUnit  float64 `json:"margin_per_unit"`
	MarginPct      float64 `json:"margin_pct"`
	UnitsOrdered   int32   `json:"units_ordered"`
	UnitsCompleted int32   `json:"units_completed"`
	UnitsFailed    int32   `json:"units_failed"`
	Revenue        float64 `json:"revenue"`
	Profit         float64 `json:"profit"`
}

// designPerformance returns per-product analytics: unit economics (CP, selling
// price, margin) and the units ordered/completed/failed for this design's SKU
// across the production pipeline, with revenue and profit. Guarded by design:read
// and the /:id brand-access gate.
func (s *Server) designPerformance(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	d, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	// Unit economics from the latest pricing (absent until the design is priced).
	var cp, sp float64
	hasPricing := false
	if p, perr := s.store.Q.GetDesignPricing(ctx, id); perr == nil {
		hasPricing = true
		cp = p.DesignCp
		switch {
		case p.ApprovedSp != nil:
			sp = float64(*p.ApprovedSp)
		case p.RecommendedSp != nil:
			sp = float64(*p.RecommendedSp)
		}
	}

	// Sales/production counts for the catalog SKU (zero until orders exist).
	var ordered, completed, failed int32
	sku := ""
	if d.Sku != nil {
		sku = *d.Sku
	}
	if sku != "" {
		if sum, serr := s.store.Q.GetDesignSalesSummary(ctx, &sku); serr == nil {
			ordered, completed, failed = sum.UnitsOrdered, sum.UnitsCompleted, sum.UnitsFailed
		}
	}

	marginPerUnit := sp - cp
	marginPct := 0.0
	if sp > 0 {
		marginPct = marginPerUnit / sp
	}
	c.JSON(http.StatusOK, performanceResponse{
		SKU:            sku,
		HasPricing:     hasPricing,
		DesignCP:       cp,
		SellingPrice:   sp,
		MarginPerUnit:  marginPerUnit,
		MarginPct:      marginPct,
		UnitsOrdered:   ordered,
		UnitsCompleted: completed,
		UnitsFailed:    failed,
		Revenue:        float64(ordered) * sp,
		Profit:         float64(ordered) * marginPerUnit,
	})
}
