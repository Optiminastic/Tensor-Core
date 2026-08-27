package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// importShopifyOrder upserts one order and rebuilds its line items from
// scratch (so a repeated sync never accumulates duplicates - Shopify's
// payload doesn't carry a product image, so product_image_url is left null
// for real orders), then - if the production queue is wired up - schedules
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
	return order, err
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
