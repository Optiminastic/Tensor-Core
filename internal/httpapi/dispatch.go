package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// orderStatusCancelled mirrors orders.status. Nothing sets it yet (the Shopify
// import does not map cancelled_at), so this guard is inert today - but it is
// the correct place for it, and it costs one comparison.
const orderStatusCancelled = "cancelled"

type dispatchResponse struct {
	ID             string     `json:"id"`
	OrderID        string     `json:"order_id"`
	Carrier        *string    `json:"carrier"`
	TrackingNumber *string    `json:"tracking_number"`
	Status         string     `json:"status"`
	DispatchedAt   *time.Time `json:"dispatched_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func dispatchDTO(d gen.DispatchOrder) dispatchResponse {
	return dispatchResponse{
		ID: d.ID.String(), OrderID: d.OrderID.String(), Carrier: d.Carrier,
		TrackingNumber: d.TrackingNumber, Status: d.Status, DispatchedAt: db.TimePtr(d.DispatchedAt),
		CreatedAt: db.Time(d.CreatedAt), UpdatedAt: db.Time(d.UpdatedAt),
	}
}

func (s *Server) registerDispatch(r *gin.Engine) {
	g := r.Group("/dispatch-orders")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.DispatchRead.Key()), s.listDispatch)
	g.POST("", s.guards.RequirePermission(auth.DispatchManage.Key()), s.createDispatch)
	g.GET("/:id", s.guards.RequirePermission(auth.DispatchRead.Key()), s.getDispatch)
	g.POST("/:id/dispatch", s.guards.RequirePermission(auth.DispatchManage.Key()), s.markDispatched)
}

func (s *Server) listDispatch(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !page.paginate {
		rows, err := s.store.Q.ListDispatch(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list dispatch orders.")
			return
		}
		out := make([]dispatchResponse, 0, len(rows))
		for _, d := range rows {
			out = append(out, dispatchDTO(d))
		}
		c.JSON(http.StatusOK, out)
		return
	}
	rows, err := s.store.Q.ListDispatchPage(ctx, gen.ListDispatchPageParams{
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list dispatch orders.")
		return
	}
	out := make([]dispatchResponse, 0, len(rows))
	for _, d := range rows {
		out = append(out, dispatchDTO(d))
	}
	if n := len(rows); n > 0 {
		setNextCursor(c, n, page.limit, db.Time(rows[n-1].CreatedAt), rows[n-1].ID)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getDispatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := s.store.Q.GetDispatchByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That dispatch order does not exist.", "Could not load the dispatch order.")
		return
	}
	c.JSON(http.StatusOK, dispatchDTO(d))
}

type createDispatchRequest struct {
	OrderID        string  `json:"order_id" binding:"required"`
	Carrier        *string `json:"carrier"`
	TrackingNumber *string `json:"tracking_number"`
}

func (s *Server) createDispatch(c *gin.Context) {
	var req createDispatchRequest
	if !bindJSON(c, &req) {
		return
	}
	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The order_id is not a valid identifier.")
		return
	}
	ctx := c.Request.Context()
	order, err := s.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		dbError(c, err, "That order does not exist.", "Could not load the order.")
		return
	}
	// A dispatch is an order-level promise, but production is per job: an order
	// is only ready once every one of its products has been packed. Without
	// this an order could be booked - and marked dispatched - while jobs were
	// still queued.
	if order.Status == orderStatusCancelled {
		detail(c, http.StatusConflict, "That order was cancelled and cannot be dispatched.")
		return
	}
	pending, err := s.store.Q.CountJobsNotPackagedForOrder(ctx, &orderID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check the order's products.")
		return
	}
	if pending > 0 {
		total, err := s.store.Q.CountJobsForOrderTotal(ctx, &orderID)
		if err != nil {
			total = pending
		}
		detail(c, http.StatusConflict, fmt.Sprintf(
			"%d of %d products are not packed yet.", pending, total))
		return
	}
	d, err := s.store.Q.InsertDispatch(ctx, gen.InsertDispatchParams{
		ID: uuid.New(), OrderID: orderID, Carrier: req.Carrier,
		TrackingNumber: req.TrackingNumber, Status: "pending",
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the dispatch order.")
		return
	}
	c.JSON(http.StatusCreated, dispatchDTO(d))
}

func (s *Server) markDispatched(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetDispatchByID(ctx, id); err != nil {
		dbError(c, err, "That dispatch order does not exist.", "Could not load the dispatch order.")
		return
	}
	d, err := s.store.Q.MarkDispatched(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not mark the dispatch order as dispatched.")
		return
	}
	c.JSON(http.StatusOK, dispatchDTO(d))
}
