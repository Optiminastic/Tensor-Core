package shopify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRecentOrders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{"nodes":[
			{
				"id": "gid://shopify/Order/5001",
				"name": "#1001",
				"displayFinancialStatus": "PAID",
				"totalPriceSet": {"shopMoney": {"amount": "1499.00", "currencyCode": "INR"}},
				"lineItems": {"nodes": [
					{
						"sku": "ART-UNICORN-01",
						"name": "Unicorn",
						"title": "Unicorn",
						"quantity": 2,
						"product": {"id": "gid://shopify/Product/7001"},
						"customAttributes": [{"key": "material", "value": "PLA"}]
					}
				]}
			},
			{
				"id": "gid://shopify/Order/5002",
				"name": "#1002",
				"displayFinancialStatus": "PENDING",
				"totalPriceSet": {"shopMoney": {"amount": "999.00", "currencyCode": "INR"}},
				"lineItems": {"nodes": []}
			}
		]}}}`))
	}))
	defer server.Close()

	orders, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("got %d orders, want 2", len(orders))
	}

	paid := orders[0]
	if paid.ID != 5001 || paid.Name != "#1001" || paid.FinancialStatus != "paid" {
		t.Errorf("unexpected paid order: %+v", paid)
	}
	if paid.TotalPrice != "1499.00" || paid.Currency != "INR" {
		t.Errorf("unexpected price/currency: %+v", paid)
	}
	if paid.Customer != nil {
		t.Errorf("customer should be unset (no read_customers scope): %+v", paid.Customer)
	}
	if len(paid.LineItems) != 1 {
		t.Fatalf("got %d line items, want 1", len(paid.LineItems))
	}
	li := paid.LineItems[0]
	if li.SKU != "ART-UNICORN-01" || li.Quantity != 2 {
		t.Errorf("unexpected line item: %+v", li)
	}
	if li.ProductID == nil || *li.ProductID != 7001 {
		t.Errorf("unexpected product id: %+v", li.ProductID)
	}
	if len(li.Properties) != 1 || li.Properties[0].Name != "material" || li.Properties[0].Value != "PLA" {
		t.Errorf("unexpected properties: %+v", li.Properties)
	}

	pending := orders[1]
	if pending.FinancialStatus != "pending" || pending.Customer != nil || len(pending.LineItems) != 0 {
		t.Errorf("unexpected pending order: %+v", pending)
	}
}

func TestListRecentOrdersClampsLimit(t *testing.T) {
	var seenFirst float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if v, ok := req.Variables["first"].(float64); ok {
			seenFirst = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{"nodes":[]}}}`))
	}))
	defer server.Close()

	if _, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 10_000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenFirst != maxBackfillOrders {
		t.Errorf("first = %v, want %v", seenFirst, maxBackfillOrders)
	}
}

func TestGIDNumericID(t *testing.T) {
	cases := map[string]int64{
		"gid://shopify/Order/123": 123,
		"gid://shopify/Order/0":   0,
		"":                        0,
		"not-a-gid":               0,
	}
	for gid, want := range cases {
		if got := gidNumericID(gid); got != want {
			t.Errorf("gidNumericID(%q) = %d, want %d", gid, got, want)
		}
	}
}
