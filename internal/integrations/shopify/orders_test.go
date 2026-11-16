package shopify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

	orders, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 0, time.Time{})
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

	if _, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 10_000, time.Time{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenFirst != ordersPageSize {
		t.Errorf("first = %v, want %v", seenFirst, ordersPageSize)
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

// Paging is what makes "Sync fetches all the latest orders" true. The store has
// 177 orders in the sync window today and gains more every day; without
// following the cursor the sync would silently stop at 250 and nobody would be
// told that the newest orders were the ones missing.
func TestListRecentOrdersFollowsPages(t *testing.T) {
	var seenAfter []any
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenAfter = append(seenAfter, body.Variables["after"])

		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			_, _ = w.Write([]byte(`{"data":{"orders":{
				"pageInfo":{"hasNextPage":true,"endCursor":"CURSOR_1"},
				"nodes":[{"id":"gid://shopify/Order/1","name":"#1","lineItems":{"nodes":[]}}]
			}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"orders":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{"id":"gid://shopify/Order/2","name":"#2","lineItems":{"nodes":[]}}]
		}}}`))
	}))
	defer server.Close()

	orders, err := testClient(server.URL).
		ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 0, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("got %d orders across pages, want 2", len(orders))
	}
	if page != 2 {
		t.Errorf("made %d requests, want 2", page)
	}
	// The first request must not send a cursor, and the second must send the
	// one the first returned - a cursor sent on page 1, or ignored on page 2,
	// silently re-reads the same page forever.
	if len(seenAfter) != 2 || seenAfter[0] != nil || seenAfter[1] != "CURSOR_1" {
		t.Errorf("after values = %v, want [<nil> CURSOR_1]", seenAfter)
	}
}

// A page that claims more but returns nothing must not spin to the ceiling.
func TestListRecentOrdersStopsOnEmptyPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{
			"pageInfo":{"hasNextPage":true,"endCursor":"ALWAYS"},"nodes":[]
		}}}`))
	}))
	defer server.Close()

	if _, err := testClient(server.URL).
		ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 0, time.Time{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests against an always-empty page, want 1", requests)
	}
}

func TestABundleLineTakesTheBundlesFields(t *testing.T) {
	var n orderNode
	raw := `{"id":"gid://shopify/Order/1","name":"T3DPS-116750","lineItems":{"nodes":[
		{"sku":"SC-ROSE","title":"Soulmate COMBO - 3D Rose (part)","quantity":1,"customAttributes":[],
		 "lineItemGroup":{"title":"Soulmate COMBO","customAttributes":[{"key":"STEP 4 - Name On 3D Rose","value":"EFAWF"}]}},
		{"sku":"DNP-RED","title":"Dual Name Plank","quantity":1,"customAttributes":[{"key":"STEP 4-First Name","value":"A"}]}
	]}}`
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := toOrderSummary(n).LineItems
	if !got[0].FromBundle || got[0].Bundle != "Soulmate COMBO" || len(got[0].Properties) != 1 ||
		got[0].Properties[0].Value != "EFAWF" {
		t.Errorf("bundle line = %+v, want the bundle's field", got[0])
	}
	if got[1].FromBundle || got[1].Bundle != "" || got[1].Properties[0].Value != "A" {
		t.Errorf("a line sold on its own changed: %+v", got[1])
	}
}

// The order list asks only whether a line is in a bundle; the bundle's fields
// come from a second, per-order query, asked only for orders that have one.
func TestListRecentOrdersReadsBundleFieldsSeparately(t *testing.T) {
	var bundleQueries int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "OrderBundles") {
			bundleQueries++
			_, _ = w.Write([]byte(`{"data":{"order":{"lineItems":{"nodes":[
				{"lineItemGroup":{"id":"gid://shopify/LineItemGroup/9","title":"Soulmate COMBO",
				  "customAttributes":[{"key":"STEP 4 - Name On 3D Rose","value":"EFAWF"}]}},
				{"lineItemGroup":null}
			]}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"orders":{"nodes":[
			{"id":"gid://shopify/Order/1","name":"T3DPS-116750","lineItems":{"nodes":[
				{"sku":"SC-ROSE","quantity":1,"customAttributes":[],"lineItemGroup":{"id":"gid://shopify/LineItemGroup/9"}},
				{"sku":"DNP-RED","quantity":1,"customAttributes":[{"key":"STEP 4-First Name","value":"A"}]}
			]}},
			{"id":"gid://shopify/Order/2","name":"T3DPS-116751","lineItems":{"nodes":[
				{"sku":"DNP-BLU","quantity":1,"customAttributes":[]}
			]}}
		]}}}`))
	}))
	defer server.Close()

	orders, err := testClient(server.URL).ListRecentOrders(context.Background(), "shop.myshopify.com", "tok", 0, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if bundleQueries != 1 {
		t.Fatalf("asked for bundle fields %d times, want once - only the order that has a bundle", bundleQueries)
	}
	rose := orders[0].LineItems[0]
	if !rose.FromBundle || rose.Bundle != "Soulmate COMBO" || len(rose.Properties) != 1 || rose.Properties[0].Value != "EFAWF" {
		t.Errorf("bundle line = %+v, want the bundle's field", rose)
	}
	if plank := orders[0].LineItems[1]; plank.FromBundle || plank.Properties[0].Value != "A" {
		t.Errorf("a line sold on its own changed: %+v", plank)
	}
}
