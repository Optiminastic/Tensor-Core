package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// importShopifyOrder upserts one order and rebuilds its line items from
// scratch (so a repeated sync never accumulates duplicates), then - if the
// production queue is wired up - schedules
// job creation in the same transaction, so nothing commits while silently
// failing to schedule it. Every order enters Tensor through here, and the
// only thing that calls it is a pull from Shopify's API
// (fetchAndImportShopifyOrders): the on-demand "Sync from Shopify" button and
// the one-time catch-up right after a store connects. There is deliberately
// no webhook path - orders arrive when someone asks for them, never on
// Shopify's schedule. connID is nullable: a sync sourced from a brand's
// already-connected brand_connections row (see connections.go's
// syncShopifyOrders) has no shopify_connections row to reference.
func (s *Server) importShopifyOrder(
	ctx context.Context, connID *uuid.UUID, payload shopifyOrderPayload, fallbackStatus string,
) (gen.Order, error) {
	items := mapShopifyLineItems(payload.LineItems)
	lineItemsJSON, err := json.Marshal(items)
	if err != nil {
		return gen.Order{}, err
	}

	var order gen.Order
	err = s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		var err error
		order, err = q.UpsertPaidOrder(ctx, gen.UpsertPaidOrderParams{
			ID: uuid.New(), ShopConnectionID: connID, ShopifyOrderID: payload.ID,
			OrderNumber: payload.Name, CustomerName: payload.customerName(),
			ShopifyCustomerID: payload.customerID(), CustomerEmail: payload.customerEmail(),
			CustomerPhone:   payload.customerPhone(),
			FinancialStatus: defaultStr(payload.FinancialStatus, fallbackStatus),
			TotalPrice:      parsePrice(payload.TotalPrice), Currency: defaultStr(payload.Currency, "USD"),
			LineItems: lineItemsJSON, Status: "queued",
			PlacedAt: payload.placedAt(), Note: nonEmptyPtr(payload.Note),
			Attributes:        payload.orderAttributes(),
			Tags:              jsonOrEmpty(payload.Tags, "[]"),
			FulfillmentStatus: nonEmptyPtr(payload.FulfillmentStatus),
			DeliveryStatus:    nonEmptyPtr(payload.DeliveryStatus),
			ReturnStatus:      nonEmptyPtr(payload.ReturnStatus),
			SourceName:        nonEmptyPtr(payload.SourceName),
			SubtotalPrice:     moneyNumeric(payload.SubtotalPrice),
			TotalDiscounts:    moneyNumeric(payload.TotalDiscounts),
			TotalShipping:     moneyNumeric(payload.TotalShipping),
			TotalReceived:     moneyNumeric(payload.TotalReceived),
			DiscountTitle:     nonEmptyPtr(payload.DiscountTitle),
			ShippingTitle:     nonEmptyPtr(payload.ShippingTitle),
			ShippingAddress:   addressJSON(payload.ShippingAddress),
			BillingAddress:    addressJSON(payload.BillingAddress),
		})
		if err != nil {
			return err
		}

		if err := q.DeleteLineItemsForOrder(ctx, order.ID); err != nil {
			return err
		}
		for _, li := range payload.LineItems {
			if _, err := q.InsertOrderLineItem(ctx, gen.InsertOrderLineItemParams{
				ID: uuid.New(), OrderID: order.ID, ShopifyOrderID: payload.ID,
				ShopifyCustomerID: payload.customerID(), Sku: nonEmptyPtr(li.SKU),
				ProductName: firstNonEmpty(li.Name, li.Title, "Unknown product"),
				Quantity:    int32(li.Quantity),
				// The pull now asks Shopify for the line's image, so this is no
				// longer null for real orders - it is the thumbnail an operator
				// recognises the product by.
				ProductImageUrl: nonEmptyPtr(li.ImageURL),
			}); err != nil {
				return err
			}
		}

		if s.jobEnqueuer != nil {
			if err := s.jobEnqueuer.EnqueueTx(ctx, tx, production.CreateJobsArgs{OrderID: order.ID}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return order, err
	}

	// An order Shopify reports as fulfilled has left the building, so its jobs
	// are no longer work: they are completed and taken off any bed still open to
	// change. Done here rather than on a status transition because orders are
	// pulled, not pushed - there is no "it just shipped" event to hook, and the
	// reconcile is idempotent so running it every import is harmless.
	//
	// After the transaction, deliberately: it touches jobs and batches, and
	// holding the order write open across that would make an import wait on the
	// batch tables.
	s.reconcileFulfilledOrder(ctx, order)
	return order, nil
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

	// The rest of Shopify's order page. Money stays in Shopify's decimal-string
	// form until the moment it is written, so nothing is rounded in transit.
	CreatedAt         string                `json:"created_at"`
	Note              string                `json:"note"`
	Attributes        []shopifyLineProp     `json:"note_attributes"`
	Tags              []string              `json:"tags"`
	FulfillmentStatus string                `json:"fulfillment_status"`
	DeliveryStatus    string                `json:"delivery_status"`
	ReturnStatus      string                `json:"return_status"`
	SourceName        string                `json:"source_name"`
	SubtotalPrice     string                `json:"subtotal_price"`
	TotalDiscounts    string                `json:"total_discounts"`
	TotalShipping     string                `json:"total_shipping"`
	TotalReceived     string                `json:"total_received"`
	DiscountTitle     string                `json:"discount_title"`
	ShippingTitle     string                `json:"shipping_title"`
	Email             string                `json:"email"`
	Phone             string                `json:"phone"`
	ShippingAddress   *shopify.OrderAddress `json:"shipping_address"`
	BillingAddress    *shopify.OrderAddress `json:"billing_address"`
}

type shopifyCustomer struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
}

func (p shopifyOrderPayload) customerName() *string {
	if p.Customer == nil {
		return nil
	}
	name := strings.TrimSpace(p.Customer.FirstName + " " + p.Customer.LastName)
	return nonEmptyPtr(name)
}

func (p shopifyOrderPayload) customerID() *int64 {
	if p.Customer == nil || p.Customer.ID == 0 {
		return nil
	}
	id := p.Customer.ID
	return &id
}

func (p shopifyOrderPayload) customerEmail() *string {
	if p.Customer == nil {
		return nil
	}
	return nonEmptyPtr(p.Customer.Email)
}

func (p shopifyOrderPayload) customerPhone() *string {
	if p.Customer == nil {
		return nil
	}
	return nonEmptyPtr(p.Customer.Phone)
}

type shopifyLineItem struct {
	ProductID  *int64            `json:"product_id"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	Title      string            `json:"title"`
	Quantity   int               `json:"quantity"`
	Properties []shopifyLineProp `json:"properties"`

	VariantTitle        string `json:"variant_title"`
	ImageURL            string `json:"image_url"`
	UnitPrice           string `json:"price"`
	DiscountedUnitPrice string `json:"discounted_price"`
	LineTotal           string `json:"line_total"`
}

type shopifyLineProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// mapShopifyLineItems turns Shopify line items into production line items,
// reading the print facts and personalisation from each line's custom
// properties.
//
// Two things happen here, and the difference matters. Every property is kept
// verbatim on LineItem.Properties - nothing a customer typed is ever discarded.
// On top of that, the keys below are RECOGNISED into typed fields so the
// pipeline can act on them (a job knows it needs personalisation, the operator
// gets a name to engrave).
//
// The recognised list is necessarily a guess at how a given store names things,
// and it will not cover every store. That used to mean unmatched personalisation
// vanished; now an unrecognised key still reaches the operator as a property,
// it just isn't machine-readable until its name is added here.
func mapShopifyLineItems(items []shopifyLineItem) []production.LineItem {
	out := make([]production.LineItem, 0, len(items))
	for _, li := range items {
		props := lineProps(li.Properties)
		// The "step" spellings come from a real storefront personaliser, which
		// numbers its questions: "STEP 4-First Name-:", "STEP 5-Second Name:".
		// Matching is on a normalised key (see lineProps), so punctuation and
		// spacing in the store's labels do not have to be reproduced exactly.
		name := prop(props,
			"personalisation_name", "custom_name", "name", "engraving_text",
			"step 4 first name", "first name", "step 3")
		second := prop(props, "step 5 second name", "second name")
		font := prop(props, "personalisation_font", "font")
		colour := prop(props, "personalisation_colour", "personalisation_color")
		variant := prop(props, "personalisation_variant", "variant")
		photo := prop(props, "personalisation_photo_url", "photo_url", "uploaded_photo")

		// Two names on one plank is one engraving job, not two. Joined rather
		// than dropped, because the second name is half of what the customer
		// bought.
		if second != nil {
			joined := *second
			if name != nil {
				joined = *name + " & " + *second
			}
			name = &joined
		}

		out = append(out, production.LineItem{
			ProductID:                    productID(li),
			SKU:                          nonEmptyPtr(li.SKU),
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
			Properties:                   keepProperties(li.Properties),
			VariantTitle:                 nonEmptyPtr(li.VariantTitle),
			ImageURL:                     nonEmptyPtr(li.ImageURL),
			UnitPrice:                    nonEmptyPtr(li.UnitPrice),
			DiscountedUnitPrice:          nonEmptyPtr(li.DiscountedUnitPrice),
			LineTotal:                    nonEmptyPtr(li.LineTotal),
		})
	}
	return out
}

// lineProps indexes properties for lookup, under both their exact lowercased
// name and a normalised form.
//
// Normalising matters because storefront personalisers label questions for
// humans, not for parsers: "STEP 4-First Name-:" and "STEP 4 First Name" are
// the same question. Punctuation is reduced to single spaces so the recognised
// list can be written readably instead of as a set of exact strings that break
// when someone edits a label.
func lineProps(props []shopifyLineProp) map[string]string {
	m := make(map[string]string, len(props)*2)
	for _, p := range props {
		exact := strings.ToLower(strings.TrimSpace(p.Name))
		m[exact] = p.Value
		if norm := normalisePropKey(exact); norm != "" && norm != exact {
			// First spelling wins: an exact match should never be displaced by
			// a normalised collision.
			if _, taken := m[norm]; !taken {
				m[norm] = p.Value
			}
		}
	}
	return m
}

// normalisePropKey reduces a human-facing label to lowercase words separated by
// single spaces: "STEP 4-First Name-:" becomes "step 4 first name".
//
// It lowercases internally rather than trusting the caller to have done it.
// An earlier version kept only a-z and silently DELETED uppercase letters, so
// "STEP 6 - WhatsApp Number:" came out as "6 hats pp umber" - a mangling that
// still looked like a plausible key and would have quietly failed to match.
func normalisePropKey(key string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// keepProperties copies Shopify's attributes onto the line item unchanged.
//
// Order is preserved because the store's numbered steps are the order the
// customer answered them in, and reading "STEP 5" above "STEP 3" would be
// needlessly confusing for whoever fulfils the order.
func keepProperties(props []shopifyLineProp) []production.LineProp {
	if len(props) == 0 {
		return nil
	}
	out := make([]production.LineProp, 0, len(props))
	for _, p := range props {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		out = append(out, production.LineProp{Name: name, Value: p.Value})
	}
	return out
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

// --- Shopify order detail -----------------------------------------------------
//
// The helpers below turn one Shopify order payload into the columns migration
// 0061 added. They all share a rule: an absent value stays absent. Shopify
// sends "" for a field it has nothing for, and writing that as an empty string
// or a zero would make the page claim a fact the store never stated.

// placedAt parses Shopify's RFC3339 createdAt.
//
// An unparseable date is left null rather than defaulted to now(): "we do not
// know when this was placed" is true, and stamping the import time onto it
// would quietly put the order on the wrong day.
func (p shopifyOrderPayload) placedAt() pgtype.Timestamptz {
	t, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// moneyNumeric parses one of Shopify's decimal money strings.
//
// Invalid or empty is null, not zero. "₹0.00 shipping" and "we were not told
// what shipping cost" render differently and mean different things.
func moneyNumeric(s string) pgtype.Numeric {
	var n pgtype.Numeric
	if strings.TrimSpace(s) == "" {
		return n
	}
	if err := n.Scan(strings.TrimSpace(s)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}

// jsonOrEmpty marshals v, falling back to the given empty literal.
//
// The columns are NOT NULL DEFAULT '[]', so they need valid JSON even when
// there is nothing to say.
func jsonOrEmpty(v any, empty string) []byte {
	if v == nil {
		return []byte(empty)
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return []byte(empty)
	}
	return b
}

// addressJSON marshals an address, or null when there is none to store.
func addressJSON(a *shopify.OrderAddress) []byte {
	if a == nil {
		return nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	return b
}

// orderAttributes keeps Shopify's order-level custom attributes verbatim, the
// same treatment line-item properties get - this is the "Additional details"
// panel, and Tensor has no idea which keys a given store considers meaningful.
func (p shopifyOrderPayload) orderAttributes() []byte {
	return jsonOrEmpty(keepProperties(p.Attributes), "[]")
}
