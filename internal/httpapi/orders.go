package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// orderResponse is the list shape of an imported Shopify order. line_items is
// carried only on the detail response.
type orderResponse struct {
	ID              string    `json:"id"`
	ShopifyOrderID  int64     `json:"shopify_order_id"`
	OrderNumber     string    `json:"order_number"`
	CustomerName    *string   `json:"customer_name"`
	FinancialStatus string    `json:"financial_status"`
	TotalPrice      float64   `json:"total_price"`
	Currency        string    `json:"currency"`
	Status          string    `json:"status"`
	ImportedAt      time.Time `json:"imported_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type orderDetailResponse struct {
	orderResponse
	LineItems json.RawMessage `json:"line_items"`
}

func orderDTO(o gen.Order) orderResponse {
	return orderResponse{
		ID: o.ID.String(), ShopifyOrderID: o.ShopifyOrderID, OrderNumber: o.OrderNumber,
		CustomerName: o.CustomerName, FinancialStatus: o.FinancialStatus,
		TotalPrice: db.NumFloat(o.TotalPrice), Currency: o.Currency, Status: o.Status,
		ImportedAt: db.Time(o.ImportedAt), CreatedAt: db.Time(o.CreatedAt), UpdatedAt: db.Time(o.UpdatedAt),
	}
}

func (s *Server) registerOrders(r *gin.Engine) {
	g := r.Group("/orders")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.OrderRead.Key()), s.listOrders)
	g.GET("/:id", s.guards.RequirePermission(auth.OrderRead.Key()), s.getOrder)
}

func (s *Server) listOrders(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	if !page.paginate {
		rows, err := s.store.Q.ListOrders(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list orders.")
			return
		}
		out := make([]orderResponse, 0, len(rows))
		for _, o := range rows {
			out = append(out, orderDTO(o))
		}
		c.JSON(http.StatusOK, out)
		return
	}

	rows, err := s.store.Q.ListOrdersPage(ctx, gen.ListOrdersPageParams{
		CursorImportedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list orders.")
		return
	}
	out := make([]orderResponse, 0, len(rows))
	for _, o := range rows {
		out = append(out, orderDTO(o))
	}
	if n := len(rows); n > 0 {
		last := rows[n-1]
		setNextCursor(c, n, page.limit, db.Time(last.ImportedAt), last.ID)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getOrder(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	o, err := s.store.Q.GetOrderByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That order does not exist.", "Could not load the order.")
		return
	}
	c.JSON(http.StatusOK, orderDetailResponse{orderResponse: orderDTO(o), LineItems: json.RawMessage(o.LineItems)})
}
