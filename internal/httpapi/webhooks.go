package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxWebhookBytes caps a webhook body so a malicious sender cannot exhaust memory.
const maxWebhookBytes = 2 << 20 // 2 MiB

func (s *Server) registerWebhooks(r *gin.Engine) {
	g := r.Group("/webhooks/shopify")
	// Authenticated by the request HMAC, not a user token.
	g.POST("/orders-paid", s.ordersPaidWebhook)
	// Shopify's mandatory GDPR compliance webhooks (required for every public app).
	s.registerComplianceWebhooks(g)
}

// ordersPaidWebhook receives Shopify's orders/paid callback, verifies the body
// HMAC, resolves the store, and upserts the order (idempotent on replay).
func (s *Server) ordersPaidWebhook(c *gin.Context) {
	if s.cfg.ShopifyClientSecret == "" {
		detail(c, http.StatusServiceUnavailable, "The Shopify integration is not configured.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBytes))
	if err != nil {
		detail(c, http.StatusBadRequest, "Could not read the webhook body.")
		return
	}
	if !shopify.VerifyWebhookHMAC(body, c.GetHeader("X-Shopify-Hmac-SHA256"), s.cfg.ShopifyClientSecret) {
		detail(c, http.StatusUnauthorized, "Invalid webhook signature.")
		return
	}
	shopDomain := c.GetHeader("X-Shopify-Shop-Domain")
	ctx := c.Request.Context()
	conn, err := s.store.Q.GetActiveConnectionByDomain(ctx, shopDomain)
	if err != nil {
		detail(c, http.StatusNotFound, "No active connection for that store.")
		return
	}

	var payload shopifyOrderPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		detail(c, http.StatusBadRequest, "The webhook payload is invalid.")
		return
	}
	items := mapShopifyLineItems(payload.LineItems)
	lineItemsJSON, err := json.Marshal(items)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the order's line items.")
		return
	}

	connID := conn.ID
	params := gen.UpsertPaidOrderParams{
		ID: uuid.New(), ShopConnectionID: &connID, ShopifyOrderID: payload.ID,
		OrderNumber: payload.Name, CustomerName: payload.customerName(),
		FinancialStatus: defaultStr(payload.FinancialStatus, "paid"),
		TotalPrice:      parsePrice(payload.TotalPrice), Currency: defaultStr(payload.Currency, "USD"),
		LineItems: lineItemsJSON, Status: "queued",
	}

	// Import the order and schedule its job creation in ONE transaction, so
	// "the order arrived" and "its jobs will be created" commit together. A
	// rollback leaves neither, which is what stops an order from landing in the
	// table with nothing ever scheduled to act on it.
	//
	// Replay-safe on both halves: UpsertPaidOrder is idempotent on the unique
	// shopify_order_id, and the worker treats errJobsAlreadyCreated as success,
	// so Shopify redelivering this webhook cannot duplicate jobs.
	err = s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		order, err := q.UpsertPaidOrder(ctx, params)
		if err != nil {
			return err
		}
		if s.jobEnqueuer == nil {
			return nil // Automation not wired up: the order still imports.
		}
		return s.jobEnqueuer.EnqueueTx(ctx, tx, production.CreateJobsArgs{OrderID: order.ID})
	})
	if err != nil {
		obs.FromContext(ctx).Error("orders/paid upsert failed", "error", err)
		detail(c, http.StatusInternalServerError, "Could not import the order.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// --- Shopify order payload + line-item mapping --------------------------------

type shopifyOrderPayload struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	FinancialStatus string            `json:"financial_status"`
	TotalPrice      string            `json:"total_price"`
	Currency        string            `json:"currency"`
	Customer        *shopifyCustomer  `json:"customer"`
	LineItems       []shopifyLineItem `json:"line_items"`
}

type shopifyCustomer struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (p shopifyOrderPayload) customerName() *string {
	if p.Customer == nil {
		return nil
	}
	name := strings.TrimSpace(p.Customer.FirstName + " " + p.Customer.LastName)
	return nonEmptyPtr(name)
}

type shopifyLineItem struct {
	ProductID  *int64            `json:"product_id"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	Title      string            `json:"title"`
	Quantity   int               `json:"quantity"`
	Properties []shopifyLineProp `json:"properties"`
}

type shopifyLineProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// mapShopifyLineItems turns Shopify line items into production line items, reading
// the print facts and personalisation from each line's custom properties. The
// mapping is a best guess at the property naming a store would use (documented in
// print-queue-be); a real store's keys are adapted here in one place.
func mapShopifyLineItems(items []shopifyLineItem) []production.LineItem {
	out := make([]production.LineItem, 0, len(items))
	for _, li := range items {
		props := lineProps(li.Properties)
		name := prop(props, "personalisation_name", "custom_name", "name", "engraving_text")
		font := prop(props, "personalisation_font", "font")
		colour := prop(props, "personalisation_colour", "personalisation_color")
		variant := prop(props, "personalisation_variant", "variant")
		photo := prop(props, "personalisation_photo_url", "photo_url", "uploaded_photo")

		out = append(out, production.LineItem{
			ProductID:                    productID(li),
			SKU:                          li.SKU,
			ProductName:                  firstNonEmpty(li.Name, li.Title, "Unknown product"),
			Quantity:                     li.Quantity,
			Unit:                         "pcs",
			Material:                     prop(props, "material"),
			Colour:                       prop(props, "colour", "color", "colour_profile"),
			NozzleProfile:                prop(props, "nozzle_profile", "nozzle", "layer_profile", "print_profile"),
			FilamentGrams:                propFloat(props, "filament_grams", "filament_weight"),
			ModelFileURL:                 prop(props, "model_file_url", "print_file", "stl_file", "model file"),
			PersonalisationName:          name,
			PersonalisationFont:          font,
			PersonalisationColour:        colour,
			PersonalisationVariant:       variant,
			PersonalisationPhotoURL:      photo,
			PersonalisationRequired:      name != nil || font != nil || colour != nil || variant != nil || photo != nil,
			PersonalisationPhotoRequired: photo != nil,
			CustomerApprovalRequired:     propBool(props, "customer_approval_required"),
			CustomerApprovalReceived:     propBool(props, "customer_approval_received"),
		})
	}
	return out
}

func lineProps(props []shopifyLineProp) map[string]string {
	m := make(map[string]string, len(props))
	for _, p := range props {
		m[strings.ToLower(strings.TrimSpace(p.Name))] = p.Value
	}
	return m
}

func prop(props map[string]string, keys ...string) *string {
	for _, k := range keys {
		if v, ok := props[k]; ok && strings.TrimSpace(v) != "" {
			return &v
		}
	}
	return nil
}

func propFloat(props map[string]string, keys ...string) *float64 {
	if v := prop(props, keys...); v != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64); err == nil {
			return &f
		}
	}
	return nil
}

func propBool(props map[string]string, keys ...string) bool {
	v := prop(props, keys...)
	if v == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

func productID(li shopifyLineItem) string {
	if li.ProductID != nil {
		return strconv.FormatInt(*li.ProductID, 10)
	}
	if li.SKU != "" {
		return li.SKU
	}
	return ""
}

func parsePrice(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func defaultStr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
