package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The personalised model for one line of an order: the customer's own text
// rendered as printable geometry, with no designer opening a CAD application.
// It is a GET because it is deterministic - the same order and line always
// render the same model - so a browser, the slicer and an operator's download
// all take the same path.
func (s *Server) registerPersonalise(r *gin.Engine) {
	r.GET("/orders/:id/personalised-model",
		s.guards.RequireUser(),
		s.guards.RequirePermission(auth.OrderRead.Key()),
		s.personalisedModel)
}

func (s *Server) personalisedModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid order id.")
		return
	}
	if !s.personalise.Available() {
		detail(c, http.StatusServiceUnavailable,
			"Personalised models are not configured: OpenSCAD is not installed on this server.")
		return
	}

	ctx := c.Request.Context()
	order, err := s.store.Q.GetOrderByID(ctx, id)
	if err != nil {
		detail(c, http.StatusNotFound, "No such order.")
		return
	}

	// The order's stored line items hold both the print facts and the
	// customer's personalisation.
	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		detail(c, http.StatusInternalServerError, "This order's line items could not be read.")
		return
	}
	line := 0
	if raw := c.Query("line"); raw != "" {
		if line, err = strconv.Atoi(raw); err != nil || line < 0 {
			detail(c, http.StatusBadRequest, "line must be a line-item index.")
			return
		}
	}
	if line >= len(items) {
		detail(c, http.StatusNotFound, "This order has no such line item.")
		return
	}
	item := items[line]

	// A picture for the order page, or the file that goes to the printer -
	// same template, same parameters, so the preview cannot show one thing and
	// the printer make another.
	preview := c.Query("format") == "png"
	var out []byte
	if preview {
		out, err = s.personalise.PreviewLineItem(ctx, item, heartAssetName, 900, 600)
	} else {
		out, err = s.personalise.RenderLineItem(ctx, item, heartAssetName)
	}
	if errors.Is(err, personalise.ErrNoTemplate) {
		detail(c, http.StatusNotImplemented,
			fmt.Sprintf("%q has no personalisation template yet, so its model is still made by hand.", item.ProductName))
		return
	}
	if err != nil {
		obs.FromContext(ctx).Error("personalised model render failed",
			"order", order.OrderNumber, "product", item.ProductName, "error", err)
		detail(c, http.StatusBadGateway, "The personalised model could not be rendered.")
		return
	}

	if preview {
		c.Data(http.StatusOK, "image/png", out)
		return
	}
	filename := fmt.Sprintf("%s-line%d.stl", order.OrderNumber, line+1)
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "model/stl", out)
}

// heartAssetName is the decorative part the plank template imports. It lives in
// the shop's design folder (DESIGN_ASSET_DIR), not in this repository - the
// masters stay where the designers keep them.
const heartAssetName = "16.DNP/RED HEART.stl"
