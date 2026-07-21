package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/brandpolicy"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// /pricing has no auth guard, matching the Python router. All three routes are
// POST; a validation failure is 422.
func (s *Server) registerPricing(r *gin.Engine) {
	g := r.Group("/pricing")
	g.POST("/design-cp", s.postDesignCP)
	g.POST("/selling-price", s.postSellingPrice)
	g.POST("/status", s.postStatus)
}

type designCPRequest struct {
	Metrics     pricing.SlicerMetrics    `json:"metrics" binding:"required"`
	Assumptions *pricing.CostAssumptions `json:"assumptions"`
}

func (s *Server) postDesignCP(c *gin.Context) {
	var req designCPRequest
	if !bindJSON(c, &req) {
		return
	}
	assumptions := pricing.DefaultCostAssumptions()
	if req.Assumptions != nil {
		assumptions = *req.Assumptions
	}
	c.JSON(http.StatusOK, pricing.ComputeDesignCP(req.Metrics, assumptions))
}

type sellingPriceRequest struct {
	DesignCP   float64                     `json:"design_cp"`
	Brand      pricing.Brand               `json:"brand" binding:"required,oneof=gifting decor"`
	FixedCosts *pricing.FixedVariableCosts `json:"fixed_costs"`
	Margins    *pricing.MarginAssumptions  `json:"margins"`
}

func (s *Server) postSellingPrice(c *gin.Context) {
	var req sellingPriceRequest
	if !bindJSON(c, &req) {
		return
	}
	fixed := pricing.FixedVariableCosts{}
	if req.FixedCosts != nil {
		fixed = *req.FixedCosts
	}
	margins := pricing.DefaultMarginAssumptions()
	if req.Margins != nil {
		margins = *req.Margins
	}

	policy, err := brandpolicy.Load(c.Request.Context(), s.store.Q, req.Brand)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the brand policy.")
		return
	}
	var ladder []int
	if policy != nil {
		ladder = policy.Ladder
	}

	result, err := pricing.GenerateSellingPrice(
		req.DesignCP, fixed, req.Brand, margins, ladder, pricing.StressAdSpendPct,
	)
	if err != nil {
		// Invalid margin assumptions leave no room for cost. The Python engine
		// raises here; we surface it as a 400 with the reason.
		detail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

type statusRequest struct {
	DesignCP float64                `json:"design_cp"`
	TargetSP float64                `json:"target_sp"`
	Brand    pricing.Brand          `json:"brand" binding:"required,oneof=gifting decor"`
	Metrics  *pricing.SlicerMetrics `json:"metrics"`
}

func (s *Server) postStatus(c *gin.Context) {
	var req statusRequest
	if !bindJSON(c, &req) {
		return
	}

	policy, err := brandpolicy.Load(c.Request.Context(), s.store.Q, req.Brand)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the brand policy.")
		return
	}

	var thresholds *[2]float64
	var entryMachineHours *float64
	entryRung := pricing.GiftingEntryRung
	if policy != nil {
		t := [2]float64{policy.GreenMax, policy.YellowMax}
		thresholds = &t
		entryMachineHours = policy.EntryMachineHours
		if policy.EntryRung != nil {
			entryRung = *policy.EntryRung
		}
	}

	result, err := pricing.EvaluateStatus(
		req.DesignCP, req.TargetSP, req.Brand, req.Metrics, thresholds, entryMachineHours, entryRung,
	)
	if err != nil {
		detail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}
