package shopify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestListRecentOrdersNeverExceedsShopifysPageCap(t *testing.T) {
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

	// Asking for more than a page must not put that number on the wire -
	// Shopify rejects it outright.
	if _, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 10_000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenFirst != ordersPageSize {
		t.Errorf("first = %v, want %v", seenFirst, ordersPageSize)
	}
}

// A limit larger than one page walks the connection with the cursor Shopify
// hands back, and stops as soon as it has what was asked for - which is what
// makes a whole-store backfill and a routine sweep the same call.
func TestListRecentOrdersPagesUntilLimit(t *testing.T) {
	var requests []map[string]any
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		requests = append(requests, req.Variables)

		page++
		nodes := make([]string, 0, 2)
		for i := 0; i < 2; i++ {
			nodes = append(nodes, `{"id":"gid://shopify/Order/`+strconv.Itoa(page*10+i)+`","name":"#1"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{` +
			`"pageInfo":{"hasNextPage":true,"endCursor":"cursor-` + strconv.Itoa(page) + `"},` +
			`"nodes":[` + strings.Join(nodes, ",") + `]}}}`))
	}))
	defer server.Close()

	orders, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 6 {
		t.Fatalf("orders = %d, want 6 (three pages of two, stopping once past the limit)", len(orders))
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	if _, ok := requests[0]["after"]; ok {
		t.Error("the first page must not send a cursor")
	}
	if requests[1]["after"] != "cursor-1" || requests[2]["after"] != "cursor-2" {
		t.Errorf("cursors not threaded: %v, %v", requests[1]["after"], requests[2]["after"])
	}
}

// A store with fewer orders than asked for stops at the last page instead of
// looping on a cursor Shopify has stopped advancing.
func TestListRecentOrdersStopsWhenNoMorePages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{"pageInfo":{"hasNextPage":false,"endCursor":"c"},` +
			`"nodes":[{"id":"gid://shopify/Order/1","name":"#1"}]}}}`))
	}))
	defer server.Close()

	orders, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 || len(orders) != 1 {
		t.Errorf("calls = %d, orders = %d, want 1 and 1", calls, len(orders))
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
