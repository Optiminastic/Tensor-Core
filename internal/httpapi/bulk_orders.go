package httpapi

// Bulk orders: a business asks for "DNP x 100, SC x 28", and the shop answers
// with a quotation.
//
// Two rules shape everything here.
//
// THE MONEY IS COMPUTED HERE, never accepted from the caller. A request says
// which SKUs, how many, and what discount was agreed; the unit price comes from
// Shopify and the subtotal, discount and total are arithmetic this file does.
// A server action's arguments are client-controlled - the frontend's own rules
// say so - and a total that arrives in the request body is a total a customer
// can choose.
//
// PRICES ARE SNAPSHOTTED onto the line at save time. A quotation is a number
// somebody was given on a date, and Shopify prices move. Reading the price live
// would mean reopening last month's quotation and seeing a different total than
// the customer was quoted, with nothing on screen to say it changed. Editing
// re-snapshots, which is what makes "edit it and the quotation updates" a
// deliberate act rather than a side effect.

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// registerBulkOrders mounts the bulk-order routes.
//
// Read is pricing:read and write is pricing:generate, rather than permissions of
// their own. A quotation IS pricing - it puts a rupee figure in front of a
// customer - and those two keys already mean "may see prices" and "may set
// one". It also lands the role matrix where the shop asked: the nav leaf sits
// inside Production, whose section needs production:read, so an Operator (no
// pricing:read) never sees this page and a Designer (no production:read) never
// sees it either.
func (s *Server) registerBulkOrders(r *gin.Engine) {
	g := r.Group("/brands/:slug/bulk-orders")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.PricingRead.Key())
	manage := s.guards.RequirePermission(auth.PricingGenerate.Key())

	g.GET("", read, s.listBulkOrders)
	g.POST("", manage, s.createBulkOrder)
	g.GET("/:id", read, s.getBulkOrder)
	g.PUT("/:id", manage, s.updateBulkOrder)
	g.DELETE("/:id", manage, s.deleteBulkOrder)
	// The SKUs that may go on a line, with today's price beside each. Separate
	// from /registry so the form makes one call for exactly what it needs.
	g.GET("/sellable-skus", read, s.listSellableSKUs)

	// Approving a quotation: download the workbook it expects, fill it in, and
	// upload it. Both are manage, not read - the download tells you exactly
	// what the order promises, and the upload creates production work.
	g.GET("/:id/sheet-template", manage, s.downloadSheetTemplate)
	// Not under an id: the sample is built from the registry, not from any one
	// order, and routing it through an order would imply otherwise.
	g.GET("/sheet-sample", manage, s.downloadSheetSample)
	g.POST("/:id/approve", manage, s.approveBulkOrder)
}

// bulkOrderLineRequest is one requested product. No price: see the file comment.
type bulkOrderLineRequest struct {
	SKU      string `json:"sku" binding:"required"`
	Quantity int32  `json:"quantity" binding:"required,gt=0"`
}

// bulkOrderRequest is the whole form, for both create and update.
type bulkOrderRequest struct {
	CustomerName    string                 `json:"customer_name" binding:"required"`
	CustomerEmail   string                 `json:"customer_email"`
	CustomerPhone   string                 `json:"customer_phone"`
	Notes           string                 `json:"notes"`
	OrderDate       string                 `json:"order_date" binding:"required"`
	ValidUntil      string                 `json:"valid_until"`
	Status          string                 `json:"status"`
	DiscountPercent float64                `json:"discount_percent" binding:"gte=0,lte=100"`
	Lines           []bulkOrderLineRequest `json:"lines" binding:"required,min=1,dive"`
}

type bulkOrderLineResponse struct {
	ID          string  `json:"id"`
	VariantID   *string `json:"variant_id"`
	SKU         string  `json:"sku"`
	ProductName string  `json:"product_name"`
	Quantity    int32   `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	LineTotal   float64 `json:"line_total"`
}

type bulkOrderResponse struct {
	ID              string                  `json:"id"`
	QuotationNumber string                  `json:"quotation_number"`
	BrandSlug       string                  `json:"brand_slug"`
	CustomerName    string                  `json:"customer_name"`
	CustomerEmail   *string                 `json:"customer_email"`
	CustomerPhone   *string                 `json:"customer_phone"`
	Notes           *string                 `json:"notes"`
	OrderDate       string                  `json:"order_date"`
	ValidUntil      *string                 `json:"valid_until"`
	Status          string                  `json:"status"`
	DiscountPercent float64                 `json:"discount_percent"`
	Subtotal        float64                 `json:"subtotal"`
	DiscountAmount  float64                 `json:"discount_amount"`
	Total           float64                 `json:"total"`
	Currency        string                  `json:"currency"`
	Lines           []bulkOrderLineResponse `json:"lines"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

// sellableSKU is one option in the form's product dropdown.
type sellableSKU struct {
	// VariantID is the registry variant this SKU maps to, when there is one.
	// Null for the many Shopify SKUs the registry has never needed - a bulk
	// order may quote anything the shop sells.
	VariantID *string `json:"variant_id"`
	SKU       string  `json:"sku"`
	// ProductName is the full label a quotation line shows, product and variant
	// together. Group is just the product, so a dropdown of 507 SKUs can be
	// grouped into the 124 products they belong to rather than being one flat
	// list nobody can scan.
	ProductName string `json:"product_name"`
	Group       string `json:"group"`
	ProductCode string `json:"product_code"`
	// UnitPrice is null when Shopify has no price for this SKU, which the form
	// shows rather than hides: a line nobody can price is a line the operator
	// has to notice before sending a quotation, not after.
	UnitPrice *float64 `json:"unit_price"`
}

func (s *Server) listSellableSKUs(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	out, err := s.sellableSKUsFor(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// sellableSKUsFor is every Shopify variant that carries a SKU and a price.
//
// SHOPIFY IS THE CATALOGUE HERE, not the registry. The first version of this
// listed registry variants instead, on the reasoning that those are the
// products Tensor can cost and produce - and the result was a dropdown of
// fifteen options against a store holding 507 priced SKUs. A bulk order is a
// commercial document, not a production plan: a customer may order anything the
// shop sells, including the products Tensor has never had to render.
//
// The registry is still consulted, for one thing: where a Shopify SKU matches a
// registry variant, that variant's id is carried onto the line, so a quotation
// that later becomes production work already points at the right product. A SKU
// the registry has never heard of simply has none, which is why the column is
// nullable.
//
// Variants without a SKU are skipped - 396 of this store's 903 - because a SKU
// is what a line is identified by and what an order is later matched on. One
// without it could be quoted and then never produced.
func (s *Server) sellableSKUsFor(ctx context.Context, slug string) ([]sellableSKU, error) {
	conn, err := s.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: slug, Provider: shopifyProvider,
	})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		return nil, fmt.Errorf("this brand's Shopify isn't connected yet, so nothing can be priced")
	}
	products, err := s.shopify.ListProducts(ctx, shop, token, 0)
	if err != nil {
		return nil, fmt.Errorf("could not fetch products from Shopify")
	}

	// Registry variants by SKU, so a quotation line can point at one when it
	// exists. Best-effort: a registry that cannot be read costs the linkage,
	// not the dropdown.
	registry := map[string]uuid.UUID{}
	if variants, err := s.store.Q.ListSellableVariants(ctx); err == nil {
		for _, v := range variants {
			registry[strings.ToUpper(strings.TrimSpace(deref(v.Sku)))] = v.ID
		}
	} else {
		obs.FromContext(ctx).Warn("registry unavailable; quotation lines will not link to variants",
			"error", err)
	}

	out := make([]sellableSKU, 0, 512)
	for _, p := range products {
		for _, v := range p.Variants {
			sku := strings.TrimSpace(v.SKU)
			if sku == "" {
				continue
			}
			row := sellableSKU{
				SKU: sku, ProductName: productLabel(p.Title, v.Title),
				Group: strings.TrimSpace(p.Title), ProductCode: p.Handle,
			}
			if amount, err := strconv.ParseFloat(v.Price, 64); err == nil && v.Price != "" {
				row.UnitPrice = &amount
			}
			if id, found := registry[strings.ToUpper(sku)]; found {
				variantID := id.String()
				row.VariantID = &variantID
			}
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProductName != out[j].ProductName {
			return out[i].ProductName < out[j].ProductName
		}
		return out[i].SKU < out[j].SKU
	})
	return out, nil
}

// productLabel is how a line reads on the quotation: the product, then the
// variant when it adds anything.
//
// Shopify calls a single-variant product's only variant "Default Title", which
// is a database artefact rather than a name and must never reach a customer.
func productLabel(product, variant string) string {
	v := strings.TrimSpace(variant)
	if v == "" || strings.EqualFold(v, "Default Title") {
		return strings.TrimSpace(product)
	}
	return strings.TrimSpace(product) + " - " + v
}

func (s *Server) listBulkOrders(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListBulkOrders(ctx, slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list bulk orders.")
		return
	}
	out := make([]bulkOrderResponse, 0, len(rows))
	for _, r := range rows {
		// The list does not render lines, so they are not fetched: one query for
		// the page rather than one per row.
		out = append(out, bulkOrderDTO(r, nil))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getBulkOrder(c *gin.Context) {
	order, lines, ok := s.loadBulkOrder(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, bulkOrderDTO(order, lines))
}

func (s *Server) createBulkOrder(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	var req bulkOrderRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	priced, totals, ok := s.priceBulkOrder(c, slug, req)
	if !ok {
		return
	}
	orderDate, validUntil, ok := parseOrderDates(c, req)
	if !ok {
		return
	}
	number, err := s.nextQuotationNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not allocate a quotation number.")
		return
	}

	actor, _ := auth.UserFrom(c)
	id := uuid.New()
	var created gen.BulkOrder
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		created, err = q.InsertBulkOrder(ctx, gen.InsertBulkOrderParams{
			ID: id, QuotationNumber: number, BrandSlug: slug,
			CustomerName:  strings.TrimSpace(req.CustomerName),
			CustomerEmail: optionalText(req.CustomerEmail),
			CustomerPhone: optionalText(req.CustomerPhone),
			Notes:         optionalText(req.Notes),
			OrderDate:     orderDate, ValidUntil: validUntil,
			Status:          statusOrDraft(req.Status),
			DiscountPercent: money(req.DiscountPercent),
			Subtotal:        money(totals.subtotal),
			DiscountAmount:  money(totals.discount),
			Total:           money(totals.total),
			CreatedBy:       &actor.ID,
		})
		if err != nil {
			return err
		}
		return insertLines(ctx, q, id, priced)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the bulk order.")
		return
	}
	c.JSON(http.StatusCreated, bulkOrderDTO(created, nil))
}

func (s *Server) updateBulkOrder(c *gin.Context) {
	order, _, ok := s.loadBulkOrder(c)
	if !ok {
		return
	}
	var req bulkOrderRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	// Re-priced from today's Shopify prices, deliberately. An edit is the shop
	// re-issuing the quotation, so it should quote what things cost now - and
	// the new numbers are snapshotted again, so the document stays fixed until
	// somebody edits it next.
	priced, totals, ok := s.priceBulkOrder(c, order.BrandSlug, req)
	if !ok {
		return
	}
	orderDate, validUntil, ok := parseOrderDates(c, req)
	if !ok {
		return
	}

	var updated gen.BulkOrder
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.UpdateBulkOrder(ctx, gen.UpdateBulkOrderParams{
			ID:            order.ID,
			CustomerName:  strings.TrimSpace(req.CustomerName),
			CustomerEmail: optionalText(req.CustomerEmail),
			CustomerPhone: optionalText(req.CustomerPhone),
			Notes:         optionalText(req.Notes),
			OrderDate:     orderDate, ValidUntil: validUntil,
			Status:          statusOrDraft(req.Status),
			DiscountPercent: money(req.DiscountPercent),
			Subtotal:        money(totals.subtotal),
			DiscountAmount:  money(totals.discount),
			Total:           money(totals.total),
		})
		if err != nil {
			return err
		}
		// Replaced wholesale rather than diffed: an edit can add, remove and
		// reorder lines at once, and for a list this short a rewrite is less
		// code and fewer states than a diff.
		if err := q.DeleteBulkOrderLines(ctx, order.ID); err != nil {
			return err
		}
		return insertLines(ctx, q, order.ID, priced)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the bulk order.")
		return
	}
	lines, _ := s.store.Q.ListBulkOrderLines(ctx, order.ID)
	c.JSON(http.StatusOK, bulkOrderDTO(updated, lines))
}

func (s *Server) deleteBulkOrder(c *gin.Context) {
	order, _, ok := s.loadBulkOrder(c)
	if !ok {
		return
	}
	if err := s.store.Q.DeleteBulkOrder(c.Request.Context(), order.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the bulk order.")
		return
	}
	c.Status(http.StatusNoContent)
}

// loadBulkOrder reads the order named by the URL and checks it belongs to the
// brand in the path, so one brand's quotation cannot be read through another's.
func (s *Server) loadBulkOrder(c *gin.Context) (gen.BulkOrder, []gen.BulkOrderLine, bool) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return gen.BulkOrder{}, nil, false
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return gen.BulkOrder{}, nil, false
	}
	ctx := c.Request.Context()
	order, err := s.store.Q.GetBulkOrder(ctx, id)
	if err != nil {
		dbError(c, err, "That bulk order does not exist.", "Could not load the bulk order.")
		return gen.BulkOrder{}, nil, false
	}
	if order.BrandSlug != slug {
		// The same answer as a missing one: confirming it exists under another
		// brand leaks one quotation id at a time.
		detail(c, http.StatusNotFound, "That bulk order does not exist.")
		return gen.BulkOrder{}, nil, false
	}
	lines, err := s.store.Q.ListBulkOrderLines(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the order's products.")
		return gen.BulkOrder{}, nil, false
	}
	return order, lines, true
}

// pricedLine is one request line with its price resolved and total computed.
type pricedLine struct {
	variantID   *uuid.UUID
	sku         string
	productName string
	quantity    int32
	unitPrice   float64
	lineTotal   float64
}

type bulkTotals struct{ subtotal, discount, total float64 }

// priceBulkOrder resolves each requested SKU and does the arithmetic.
//
// Refuses an unknown or unpriced SKU rather than quoting it at zero: a
// quotation with a free line on it is worse than no quotation, and the operator
// can see which SKU is at fault from the message.
func (s *Server) priceBulkOrder(
	c *gin.Context, slug string, req bulkOrderRequest,
) ([]pricedLine, bulkTotals, bool) {
	available, err := s.sellableSKUsFor(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusBadGateway, err.Error())
		return nil, bulkTotals{}, false
	}
	bySKU := make(map[string]sellableSKU, len(available))
	for _, a := range available {
		bySKU[strings.ToUpper(a.SKU)] = a
	}

	out := make([]pricedLine, 0, len(req.Lines))
	var subtotal float64
	for i, l := range req.Lines {
		match, found := bySKU[strings.ToUpper(strings.TrimSpace(l.SKU))]
		if !found {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
				"Line %d: %q is not a product in the registry.", i+1, l.SKU))
			return nil, bulkTotals{}, false
		}
		if match.UnitPrice == nil {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
				"Line %d: %s has no price in Shopify, so it cannot be quoted.", i+1, match.SKU))
			return nil, bulkTotals{}, false
		}
		total := round2(*match.UnitPrice * float64(l.Quantity))
		subtotal += total
		line := pricedLine{
			sku: match.SKU, productName: match.ProductName,
			quantity: l.Quantity, unitPrice: *match.UnitPrice, lineTotal: total,
		}
		if match.VariantID != nil {
			if id, err := uuid.Parse(*match.VariantID); err == nil {
				line.variantID = &id
			}
		}
		out = append(out, line)
	}

	subtotal = round2(subtotal)
	discount := round2(subtotal * req.DiscountPercent / 100)
	return out, bulkTotals{
		subtotal: subtotal,
		discount: discount,
		total:    round2(subtotal - discount),
	}, true
}

func insertLines(ctx context.Context, q *gen.Queries, orderID uuid.UUID, lines []pricedLine) error {
	for i, l := range lines {
		if err := q.InsertBulkOrderLine(ctx, gen.InsertBulkOrderLineParams{
			ID: uuid.New(), BulkOrderID: orderID, VariantID: l.variantID,
			Sku: l.sku, ProductName: l.productName, Quantity: l.quantity,
			UnitPrice: money(l.unitPrice), LineTotal: money(l.lineTotal),
			Position: int32(i),
		}); err != nil {
			return err
		}
	}
	return nil
}

// nextQuotationNumber mints QUO-xxxxx, retrying on the rare collision.
//
// Random rather than sequential, matching production.NewJobNumber: a sequence
// would tell a customer how many quotations the shop has ever written.
func (s *Server) nextQuotationNumber(ctx context.Context) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		candidate := fmt.Sprintf("QUO-%05d", rand.Intn(100000))
		taken, err := s.store.Q.QuotationNumberExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free quotation number")
}

func bulkOrderDTO(o gen.BulkOrder, lines []gen.BulkOrderLine) bulkOrderResponse {
	out := bulkOrderResponse{
		ID: o.ID.String(), QuotationNumber: o.QuotationNumber, BrandSlug: o.BrandSlug,
		CustomerName: o.CustomerName, CustomerEmail: o.CustomerEmail,
		CustomerPhone: o.CustomerPhone, Notes: o.Notes,
		OrderDate: o.OrderDate.Time.Format("2006-01-02"),
		Status:    o.Status, DiscountPercent: db.NumFloat(o.DiscountPercent),
		Subtotal: db.NumFloat(o.Subtotal), DiscountAmount: db.NumFloat(o.DiscountAmount),
		Total: db.NumFloat(o.Total), Currency: o.Currency,
		Lines:     make([]bulkOrderLineResponse, 0, len(lines)),
		CreatedAt: db.Time(o.CreatedAt), UpdatedAt: db.Time(o.UpdatedAt),
	}
	if o.ValidUntil.Valid {
		v := o.ValidUntil.Time.Format("2006-01-02")
		out.ValidUntil = &v
	}
	for _, l := range lines {
		row := bulkOrderLineResponse{
			ID: l.ID.String(), SKU: l.Sku, ProductName: l.ProductName,
			Quantity: l.Quantity, UnitPrice: db.NumFloat(l.UnitPrice),
			LineTotal: db.NumFloat(l.LineTotal),
		}
		if l.VariantID != nil {
			v := l.VariantID.String()
			row.VariantID = &v
		}
		out.Lines = append(out.Lines, row)
	}
	return out
}

// parseOrderDates reads the two dates off the request. order_date is required;
// valid_until is optional and simply absent when blank.
func parseOrderDates(c *gin.Context, req bulkOrderRequest) (pgtype.Date, pgtype.Date, bool) {
	order, err := time.Parse("2006-01-02", strings.TrimSpace(req.OrderDate))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The order date must be YYYY-MM-DD.")
		return pgtype.Date{}, pgtype.Date{}, false
	}
	out := pgtype.Date{Time: order, Valid: true}

	raw := strings.TrimSpace(req.ValidUntil)
	if raw == "" {
		return out, pgtype.Date{}, true
	}
	until, err := time.Parse("2006-01-02", raw)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The valid-until date must be YYYY-MM-DD.")
		return pgtype.Date{}, pgtype.Date{}, false
	}
	if until.Before(order) {
		detail(c, http.StatusUnprocessableEntity, "The quotation cannot expire before its own date.")
		return pgtype.Date{}, pgtype.Date{}, false
	}
	return out, pgtype.Date{Time: until, Valid: true}, true
}

func statusOrDraft(status string) string {
	switch strings.TrimSpace(status) {
	case "sent", "accepted", "cancelled":
		return strings.TrimSpace(status)
	default:
		return "draft"
	}
}

// optionalText turns a blank field into NULL, so "not given" and "given as
// empty" are not two different states in the database.
func optionalText(v string) *string {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil
	}
	return &t
}

// money renders a rupee amount for a numeric column, two decimals.
//
// Through the decimal string rather than a float, matching gramsNumeric: a
// numeric column scanned from a formatted string keeps exactly the figure the
// quotation shows.
func money(v float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(v, 'f', 2, 64)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}

// round2 is money arithmetic: two decimal places, half away from zero.
//
// Applied per line and again to each total, so the figures on the quotation add
// up exactly as shown - a customer checking the arithmetic by hand gets the
// same answer.
func round2(v float64) float64 {
	return float64(int64(v*100+copySign(0.5, v))) / 100
}

func copySign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}
