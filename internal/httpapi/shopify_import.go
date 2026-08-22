package httpapi

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
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
	ProductID *int64 `json:"product_id"`
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	// What the customer chose from the product's own options - the filament
	// colour among them. Shopify's REST webhook calls this variant_title.
	VariantTitle   string            `json:"variant_title"`
	VariantOptions []shopifyLineProp `json:"variant_options"`
	Properties     []shopifyLineProp `json:"properties"`
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
		// The variant is a choice the customer made just as much as the text
		// is, so it sits alongside it on the line - an operator reading the
		// order sees "Colour: BABY PINK" without decoding the product title.
		options := append(variantOptions(li), personalisationOptions(li.Properties)...)
		// Exact keys first (a store that names its fields the documented way
		// gets exactly what it asked for), then a substring match over whatever
		// the store actually called them - "STEP 4-First Name-" is as much a
		// name field as "personalisation_name" is.
		name := firstNonNil(
			prop(props, "personalisation_name", "custom_name", "name", "engraving_text"),
			matchOption(options, "name"),
		)
		font := firstNonNil(prop(props, "personalisation_font", "font"), matchOption(options, "font"))
		colour := firstNonNil(
			prop(props, "personalisation_colour", "personalisation_color"),
			matchOption(options, "colour", "color"),
		)
		variant := firstNonNil(
			prop(props, "personalisation_variant", "variant"),
			matchOption(options, "variant"),
		)
		photo := prop(props, "personalisation_photo_url", "photo_url", "uploaded_photo")

		out = append(out, production.LineItem{
			ProductID:               productID(li),
			SKU:                     nonEmptyPtr(li.SKU),
			ProductName:             firstNonEmpty(li.Name, li.Title, "Unknown product"),
			Quantity:                li.Quantity,
			Unit:                    "pcs",
			Material:                prop(props, "material"),
			Colour:                  firstNonNil(variantColour(li), prop(props, "colour", "color", "colour_profile")),
			NozzleProfile:           prop(props, "nozzle_profile", "nozzle", "layer_profile", "print_profile"),
			FilamentGrams:           propFloat(props, "filament_grams", "filament_weight"),
			ModelFileURL:            prop(props, "model_file_url", "print_file", "stl_file", "model file"),
			PersonalisationName:     name,
			PersonalisationFont:     font,
			PersonalisationColour:   colour,
			PersonalisationVariant:  variant,
			PersonalisationPhotoURL: photo,
			Options:                 options,
			PersonalisationRequired: name != nil || font != nil || colour != nil ||
				variant != nil || photo != nil || len(options) > 0,
			PersonalisationPhotoRequired: photo != nil,
			CustomerApprovalRequired:     propBool(props, "customer_approval_required"),
			CustomerApprovalReceived:     propBool(props, "customer_approval_received"),
		})
	}
	return out
}

// colourLabel matches the variant option a store uses for the filament colour.
// It matches the word anywhere in the label because a storefront names its
// options for the customer, not for us - this shop's read "STEP 1 - Colour".
// Only variant options are tested, which are the product's own axes, so a
// stray match is unlikely.
var colourLabel = regexp.MustCompile(`(?i)colou?r`)

// variantOptions turns the variant the customer bought into labelled options.
// With product access these are Shopify's own named options; without it, the
// variant title is kept whole rather than guessed apart, because splitting
// "BABY PINK / NO LIGHT" on the slash cannot say which half is the colour.
func variantOptions(li shopifyLineItem) []production.Option {
	if len(li.VariantOptions) > 0 {
		out := make([]production.Option, 0, len(li.VariantOptions))
		for _, o := range li.VariantOptions {
			name, value := strings.TrimSpace(o.Name), strings.TrimSpace(o.Value)
			// Shopify names a single-option product's option "Title" and sets
			// it to "Default Title" when the product has no real options.
			if name == "" || value == "" || strings.EqualFold(value, "Default Title") {
				continue
			}
			out = append(out, production.Option{Name: name, Value: value})
		}
		return out
	}
	if title := strings.TrimSpace(li.VariantTitle); title != "" && !strings.EqualFold(title, "Default Title") {
		return []production.Option{{Name: "Variant", Value: title}}
	}
	return nil
}

// variantColour picks the filament colour out of the variant's named options.
// Only an explicitly named colour option counts: loading the wrong filament
// costs a print, so a guess is worse than leaving it for the operator.
func variantColour(li shopifyLineItem) *string {
	for _, o := range li.VariantOptions {
		if colourLabel.MatchString(strings.TrimSpace(o.Name)) {
			if value := strings.TrimSpace(o.Value); value != "" {
				return &value
			}
		}
	}
	return nil
}

// gpoOptionsKey is the single property a Globo Product Options line carries
// with every step the customer filled in, as one JSON object. Used only when
// the individual step properties are missing, since they are the source the
// merchant sees in Shopify admin.
const gpoOptionsKey = "_gpo_options"

// personalisationOptions returns what the customer actually typed, in the order
// the storefront asked for it. Shopify's convention is that a leading
// underscore marks an app's own bookkeeping property (_has_gpo,
// _gpo_product_group, ...), which is noise to a print operator - those are
// dropped, apart from the GPO blob used as a fallback.
func personalisationOptions(props []shopifyLineProp) []production.Option {
	out := make([]production.Option, 0, len(props))
	for _, p := range props {
		name, value := strings.TrimSpace(p.Name), strings.TrimSpace(p.Value)
		if name == "" || value == "" || strings.HasPrefix(name, "_") {
			continue
		}
		out = append(out, production.Option{Name: name, Value: value})
	}
	if len(out) > 0 {
		return out
	}
	return gpoFallbackOptions(props)
}

// gpoFallbackOptions unpacks the _gpo_options JSON blob. Its keys are sorted so
// two imports of the same order produce the same line - Go map iteration order
// is deliberately random, and an order's stored options should not churn.
func gpoFallbackOptions(props []shopifyLineProp) []production.Option {
	for _, p := range props {
		if strings.TrimSpace(p.Name) != gpoOptionsKey {
			continue
		}
		var fields map[string]string
		if err := json.Unmarshal([]byte(p.Value), &fields); err != nil {
			return nil
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			if !strings.HasPrefix(k, "_") && strings.TrimSpace(fields[k]) != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		out := make([]production.Option, 0, len(keys))
		for _, k := range keys {
			out = append(out, production.Option{Name: k, Value: strings.TrimSpace(fields[k])})
		}
		return out
	}
	return nil
}

// matchOption finds the options whose label contains any of the given words and
// joins them, so a product that asks for two names ("First Name", "Second
// Name") reaches the station as one field rather than losing the second.
func matchOption(options []production.Option, words ...string) *string {
	values := make([]string, 0, 2)
	for _, o := range options {
		label := strings.ToLower(o.Name)
		for _, w := range words {
			if strings.Contains(label, w) {
				values = append(values, o.Value)
				break
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	joined := strings.Join(values, " / ")
	return &joined
}

// firstNonNil returns the first value that was actually found.
func firstNonNil(values ...*string) *string {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
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
