package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// A real order, shaped exactly as the store's own admin page shows it:
// T3DPS-114728, two Dual Name Plank lines discounted from ₹530 to ₹424, a 20%
// order discount, free shipping, and the numbered STEP properties the
// personaliser writes. Built as a shopify.OrderSummary and pushed through the
// same toOrderPayload the live sync uses, so the mapping, the storage and the
// DTO are all exercised by one test rather than asserted from the outside.
func dualNamePlankOrder() shopify.OrderSummary {
	line := func(variant, sku, first, second string) shopify.OrderLineItem {
		return shopify.OrderLineItem{
			SKU: sku, Name: "Dual Name Plank", Title: "Dual Name Plank",
			VariantTitle: variant, Quantity: 1,
			ImageURL:  "https://cdn.shopify.com/plank.jpg",
			UnitPrice: "530.00", DiscountedUnitPrice: "424.00", LineTotal: "424.00",
			Properties: []shopify.OrderLineProp{
				{Name: "_has_gpo", Value: "622778"},
				{Name: "STEP 3-", Value: "1 RED HEART"},
				{Name: "STEP 4-First Name-", Value: first},
				{Name: "STEP 5-Second Name", Value: second},
				{Name: "STEP 6 - WhatsApp Number", Value: "8709192686"},
				{Name: "_gpo_product_group", Value: "1788151352467"},
				{Name: "_gpo_addon_price", Value: "30"},
			},
		}
	}
	return shopify.OrderSummary{
		ID: 114728, Name: "T3DPS-114728", FinancialStatus: "paid",
		FulfillmentStatus: "unfulfilled", SourceName: "Online Store",
		DeliveryStatus: "in_transit", ReturnStatus: "no_return",
		Tags:       []string{"Recovered by Wava carts"},
		CreatedAt:  "2026-08-31T10:14:00Z",
		TotalPrice: "848.00", Currency: "INR",
		SubtotalPrice: "848.00", TotalDiscounts: "212.00",
		TotalShipping: "0.00", TotalReceived: "848.00",
		DiscountTitle: "20% off for orders above ₹1000",
		ShippingTitle: "FREE DISPATCH - BEST DEAL 🎉",
		Attributes:    []shopify.OrderLineProp{{Name: "__ref_id", Value: "'-L/pL~s{P"}},
		LineItems: []shopify.OrderLineItem{
			line("SKY BLUE / NO LIGHT", "T3DPS-DNP-13", "VASU", "PADMANABH"),
			line("GOLD / NO LIGHT", "T3DPS-DNP-11", "PRIYA", "RITESH"),
		},
	}
}

func TestShopifyOrderDetailSurvivesImport(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	s := &Server{store: store}

	order, err := s.importShopifyOrder(
		context.Background(), nil, toOrderPayload(dualNamePlankOrder()), "pending")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	rr := doJSON(router, http.MethodGet, "/orders/"+order.ID.String(),
		minter.mint(t, []string{"order:read"}), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /orders/:id = %d body=%s", rr.Code, rr.Body.String())
	}

	var got struct {
		OrderNumber       string          `json:"order_number"`
		PlacedAt          *string         `json:"placed_at"`
		FinancialStatus   string          `json:"financial_status"`
		FulfillmentStatus *string         `json:"fulfillment_status"`
		SourceName        *string         `json:"source_name"`
		DeliveryStatus    *string         `json:"delivery_status"`
		ReturnStatus      *string         `json:"return_status"`
		ItemCount         int             `json:"item_count"`
		Tags              json.RawMessage `json:"tags"`
		SubtotalPrice     *string         `json:"subtotal_price"`
		TotalDiscounts    *string         `json:"total_discounts"`
		TotalShipping     *string         `json:"total_shipping"`
		TotalReceived     *string         `json:"total_received"`
		DiscountTitle     *string         `json:"discount_title"`
		ShippingTitle     *string         `json:"shipping_title"`
		Attributes        json.RawMessage `json:"attributes"`
		LineItems         []struct {
			ProductName  string  `json:"product_name"`
			SKU          *string `json:"sku"`
			VariantTitle *string `json:"variant_title"`
			ImageURL     *string `json:"image_url"`
			UnitPrice    *string `json:"unit_price"`
			LineTotal    *string `json:"line_total"`
			Properties   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"properties"`
		} `json:"line_items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for _, c := range []struct{ what, got, want string }{
		{"order_number", got.OrderNumber, "T3DPS-114728"},
		{"financial_status", got.FinancialStatus, "paid"},
		{"fulfillment_status", str(got.FulfillmentStatus), "unfulfilled"},
		{"source_name", str(got.SourceName), "Online Store"},
		{"delivery_status", str(got.DeliveryStatus), "in_transit"},
		{"return_status", str(got.ReturnStatus), "no_return"},
		{"discount_title", str(got.DiscountTitle), "20% off for orders above ₹1000"},
		{"shipping_title", str(got.ShippingTitle), "FREE DISPATCH - BEST DEAL 🎉"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}

	// Money is compared by VALUE, not by spelling. Postgres normalises a zero
	// numeric to "0" rather than "0.00", and the page formats currency itself -
	// what must survive the round trip is the amount, not its trailing zeros.
	money := func(what string, p *string, want float64) {
		t.Helper()
		if p == nil {
			t.Errorf("%s is null, want %.2f", what, want)
			return
		}
		v, err := strconv.ParseFloat(*p, 64)
		if err != nil || v != want {
			t.Errorf("%s = %q, want %.2f", what, *p, want)
		}
	}
	money("subtotal_price", got.SubtotalPrice, 848)
	money("total_discounts", got.TotalDiscounts, 212)
	money("total_shipping", got.TotalShipping, 0)
	money("total_received", got.TotalReceived, 848)

	// placed_at is the customer's date, not the import's.
	if got.PlacedAt == nil || (*got.PlacedAt)[:10] != "2026-08-31" {
		t.Errorf("placed_at = %v, want 2026-08-31", got.PlacedAt)
	}
	// Two lines of one unit each. Counted in UNITS, not lines: Shopify's own
	// list says "2 items" for one line of two, and a row count would disagree
	// with the number an operator reads in the other tab.
	if got.ItemCount != 2 {
		t.Errorf("item_count = %d, want 2", got.ItemCount)
	}
	if string(got.Tags) == "[]" {
		t.Error("order tags were dropped; the Tags column needs them")
	}
	if string(got.Attributes) == "[]" {
		t.Error("order attributes were dropped; the Additional details panel needs them")
	}

	if len(got.LineItems) != 2 {
		t.Fatalf("got %d line items, want 2", len(got.LineItems))
	}
	li := got.LineItems[0]
	for _, c := range []struct{ what, got, want string }{
		{"product_name", li.ProductName, "Dual Name Plank"},
		{"sku", str(li.SKU), "T3DPS-DNP-13"},
		{"variant_title", str(li.VariantTitle), "SKY BLUE / NO LIGHT"},
		{"image_url", str(li.ImageURL), "https://cdn.shopify.com/plank.jpg"},
		{"unit_price", str(li.UnitPrice), "530.00"},
		{"line_total", str(li.LineTotal), "424.00"},
	} {
		if c.got != c.want {
			t.Errorf("line item %s = %q, want %q", c.what, c.got, c.want)
		}
	}

	// Every property, in the order the customer answered them, hidden ones
	// included - the whole point is that nothing Shopify sent is dropped.
	if len(li.Properties) != 7 {
		t.Fatalf("got %d properties, want all 7", len(li.Properties))
	}
	want := []string{
		"_has_gpo", "STEP 3-", "STEP 4-First Name-", "STEP 5-Second Name",
		"STEP 6 - WhatsApp Number", "_gpo_product_group", "_gpo_addon_price",
	}
	for i, w := range want {
		if li.Properties[i].Name != w {
			t.Errorf("property %d = %q, want %q (order must be preserved)",
				i, li.Properties[i].Name, w)
		}
	}
	if li.Properties[2].Value != "VASU" {
		t.Errorf("first name = %q, want VASU", li.Properties[2].Value)
	}
}
