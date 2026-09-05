package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// orderResponse is the list shape of an imported Shopify order. line_items and
// products are carried only on the detail response.
type orderResponse struct {
	ID                string  `json:"id"`
	ShopifyOrderID    int64   `json:"shopify_order_id"`
	OrderNumber       string  `json:"order_number"`
	CustomerName      *string `json:"customer_name"`
	ShopifyCustomerID *int64  `json:"shopify_customer_id"`
	CustomerEmail     *string `json:"customer_email"`
	CustomerPhone     *string `json:"customer_phone"`
	FinancialStatus   string  `json:"financial_status"`
	TotalPrice        float64 `json:"total_price"`
	Currency          string  `json:"currency"`
	Status            string  `json:"status"`
	// Source is "shopify_webhook" for a real imported order or "seed" for a
	// dummy one - what the frontend's live/dummy toggle filters on.
	Source     string    `json:"source"`
	ImportedAt time.Time `json:"imported_at"`
	// Set only when the create_jobs_from_order River job exhausted its
	// attempts: what went wrong, and when it was recorded. Null on a healthy
	// order. Retrying via POST /production-jobs/from-order/:id clears both.
	JobCreationError    *string    `json:"job_creation_error"`
	JobCreationFailedAt *time.Time `json:"job_creation_failed_at"`

	// Shopify's own order page, mirrored - see migration 0061.
	//
	// PlacedAt is when the customer ordered, as against ImportedAt (when Tensor
	// first saw it); on a backfill they are weeks apart and it is the
	// customer's date that belongs on the page.
	PlacedAt          *time.Time `json:"placed_at"`
	Note              *string    `json:"note"`
	FulfillmentStatus *string    `json:"fulfillment_status"`
	// DeliveryStatus is the carrier's view and is null until something ships;
	// ReturnStatus is Shopify's own, "no_return" on an order nobody sent back.
	DeliveryStatus *string `json:"delivery_status"`
	ReturnStatus   *string `json:"return_status"`
	// ItemCount is the units on the order, summed across its lines.
	//
	// Carried on the LIST response, unlike line_items itself: the orders table
	// shows "2 items" per row, and shipping every row's full line-item jsonb
	// to render one integer would multiply the payload for nothing.
	ItemCount  int     `json:"item_count"`
	SourceName *string `json:"source_name"`
	// Money the store stated, as decimal strings rather than floats so no
	// amount is re-rounded in transit. Postgres normalises trailing zeros
	// ("0.00" comes back "0"), so these carry the VALUE - the page does the
	// currency formatting. Null means "not stated", which is not zero.
	SubtotalPrice  *string `json:"subtotal_price"`
	TotalDiscounts *string `json:"total_discounts"`
	TotalShipping  *string `json:"total_shipping"`
	// TotalReceived is the "Paid" line: what the customer has actually paid,
	// which differs from TotalPrice on a COD or partly-refunded order.
	TotalReceived *string `json:"total_received"`
	DiscountTitle *string `json:"discount_title"`
	ShippingTitle *string `json:"shipping_title"`
	// Attributes is Shopify's order-level custom attributes - its "Additional
	// details" panel - and Tags its order tags. Both verbatim: Tensor has no
	// idea which keys a given store considers meaningful.
	Attributes json.RawMessage `json:"attributes"`
	Tags       json.RawMessage `json:"tags"`
	// Protected customer data: null until Shopify grants the app protected-data
	// access, at which point a re-sync fills them in with no code change.
	ShippingAddress json.RawMessage `json:"shipping_address"`
	BillingAddress  json.RawMessage `json:"billing_address"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// orderProductResponse is one product within an order, from order_line_items -
// the commerce-facing shape (SKU, name, image), distinct from the richer
// production-facts line_items jsonb the print pipeline consumes.
type orderProductResponse struct {
	ID                string  `json:"id"`
	ShopifyOrderID    int64   `json:"shopify_order_id"`
	ShopifyCustomerID *int64  `json:"shopify_customer_id"`
	SKU               *string `json:"sku"`
	ProductName       string  `json:"product_name"`
	ProductImageURL   *string `json:"product_image_url"`
	Quantity          int32   `json:"quantity"`
}

type orderDetailResponse struct {
	orderResponse
	LineItems json.RawMessage        `json:"line_items"`
	Products  []orderProductResponse `json:"products"`
}

func orderDTO(o gen.Order) orderResponse {
	return orderResponse{
		ID: o.ID.String(), ShopifyOrderID: o.ShopifyOrderID, OrderNumber: o.OrderNumber,
		CustomerName: o.CustomerName, ShopifyCustomerID: o.ShopifyCustomerID,
		CustomerEmail: o.CustomerEmail, CustomerPhone: o.CustomerPhone,
		FinancialStatus: o.FinancialStatus,
		TotalPrice:      db.NumFloat(o.TotalPrice), Currency: o.Currency, Status: o.Status,
		Source:              o.Source,
		ImportedAt:          db.Time(o.ImportedAt),
		JobCreationError:    o.JobCreationError,
		JobCreationFailedAt: db.TimePtr(o.JobCreationFailedAt),

		PlacedAt:          db.TimePtr(o.PlacedAt),
		Note:              o.Note,
		FulfillmentStatus: o.FulfillmentStatus,
		DeliveryStatus:    o.DeliveryStatus,
		ReturnStatus:      o.ReturnStatus,
		ItemCount:         countLineItems(o.LineItems),
		SourceName:        o.SourceName,
		SubtotalPrice:     numericStr(o.SubtotalPrice),
		TotalDiscounts:    numericStr(o.TotalDiscounts),
		TotalShipping:     numericStr(o.TotalShipping),
		TotalReceived:     numericStr(o.TotalReceived),
		DiscountTitle:     o.DiscountTitle,
		ShippingTitle:     o.ShippingTitle,
		Attributes:        rawJSON(o.Attributes, "[]"),
		Tags:              rawJSON(o.Tags, "[]"),
		ShippingAddress:   rawJSON(o.ShippingAddress, "null"),
		BillingAddress:    rawJSON(o.BillingAddress, "null"),

		CreatedAt: db.Time(o.CreatedAt), UpdatedAt: db.Time(o.UpdatedAt),
	}
}

// numericStr renders a money column as the decimal string the store stated,
// or nil when it never stated one.
//
// A string, not a float: these are displayed rather than summed, and a float
// round-trip is how a total starts disagreeing with Shopify's own page by a
// paisa. Null stays null - "not stated" and "zero" are different facts.
func numericStr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	v, err := n.Value()
	if err != nil || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

// countLineItems sums the quantities on an order's lines.
//
// Quantities, not lines: Shopify's own list says "2 items" for one line of two,
// and a count of rows would disagree with the number an operator is reading in
// the other tab. Unparseable jsonb counts as zero rather than failing the whole
// list - one unreadable order must not blank the page.
func countLineItems(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var items []struct {
		Quantity int `json:"quantity"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0
	}
	total := 0
	for _, it := range items {
		total += it.Quantity
	}
	return total
}

// rawJSON passes a jsonb column through untouched, substituting a literal when
// the column is empty so the response is always valid JSON of the right shape.
func rawJSON(b []byte, empty string) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(empty)
	}
	return json.RawMessage(b)
}

func orderProductDTO(li gen.OrderLineItem) orderProductResponse {
	return orderProductResponse{
		ID: li.ID.String(), ShopifyOrderID: li.ShopifyOrderID, ShopifyCustomerID: li.ShopifyCustomerID,
		SKU: li.Sku, ProductName: li.ProductName, ProductImageURL: li.ProductImageUrl, Quantity: li.Quantity,
	}
}

// orderSource maps the frontend's live/dummy toggle to the DB's source value.
// Any other (or absent) value returns nil, so the query is unfiltered.
func orderSource(c *gin.Context) *string {
	var v string
	switch c.Query("source") {
	case "live":
		v = "shopify_webhook"
	case "dummy":
		v = "seed"
	default:
		return nil
	}
	return &v
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
	source := orderSource(c)

	// ?has_jobs=false lists orders that produced no production jobs at all -
	// the operator's view of imports that silently went nowhere. A query
	// parameter rather than its own route: Gin cannot register a static
	// /orders/without-jobs alongside the existing /orders/:id wildcard, it
	// panics at startup.
	if c.Query("has_jobs") == "false" {
		rows, err := s.store.Q.ListOrdersWithoutJobs(ctx, source)
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

	if !page.paginate {
		rows, err := s.store.Q.ListOrders(ctx, source)
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
		CursorImportedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit, Source: source,
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
	ctx := c.Request.Context()
	o, err := s.store.Q.GetOrderByID(ctx, id)
	if err != nil {
		dbError(c, err, "That order does not exist.", "Could not load the order.")
		return
	}
	items, err := s.store.Q.ListLineItemsForOrder(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the order's products.")
		return
	}
	products := make([]orderProductResponse, 0, len(items))
	for _, li := range items {
		products = append(products, orderProductDTO(li))
	}
	c.JSON(http.StatusOK, orderDetailResponse{
		orderResponse: orderDTO(o), LineItems: json.RawMessage(o.LineItems), Products: products,
	})
}
