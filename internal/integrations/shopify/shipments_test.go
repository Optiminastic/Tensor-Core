package shopify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The response below is the shape the live store returns, verified against it:
// `fulfillments` is a plain ARRAY (no nodes, no pageInfo), trackingInfo is an
// array on each fulfilment, and email can be null while phone is set.
const liveShipmentsBody = `{"data":{"orders":{
  "pageInfo":{"hasNextPage":false,"endCursor":""},
  "nodes":[{
    "id":"gid://shopify/Order/10000000000001",
    "name":"TEST-00001",
    "createdAt":"2026-10-08T07:41:26Z",
    "email":null,
    "phone":"+910000000001",
    "displayFulfillmentStatus":"FULFILLED",
    "totalPriceSet":{"shopMoney":{"amount":"649.0","currencyCode":"INR"}},
    "shippingAddress":{"name":"Test Buyer","address1":"1 Test Road","address2":"Near Test Landmark","city":"Testpur","province":"Teststate","zip":"400001","country":"India","phone":"0000000001"},
    "lineItems":{"nodes":[{"title":"Dual Name Plank","variantTitle":"BLUE / NO LIGHT","sku":"DNP-BLU","quantity":1}]},
    "fulfillments":[
      {"createdAt":"2026-10-08T10:02:45Z","displayStatus":"IN_TRANSIT","trackingInfo":[{"company":"Delhivery","number":"90000000001","url":"https://www.delhivery.com/track/package/90000000001"}]},
      {"createdAt":"2026-10-08T11:00:00Z","displayStatus":"IN_TRANSIT","trackingInfo":[{"company":"Blue Dart","number":"BD123","url":"https://bluedart.example/BD123"}]}
    ]
  }]
}}}`

func shipmentServer(t *testing.T, body string, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Shopify-Access-Token"); got != "tok" {
			t.Errorf("token header %q", got)
		}
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if seen != nil {
			*seen = payload.Variables
		}
		_, _ = w.Write([]byte(body))
	}))
}

func clientFor(server *httptest.Server) *Client {
	c := New("2024-10", 5*time.Second)
	c.baseURL = server.URL
	return c
}

// One order, two carriers: each parcel is its own row, because each has its own
// waybill and its own fate. Flattening to one row per order loses the one that
// failed.
func TestListShipmentsIsOneRowPerWaybill(t *testing.T) {
	server := shipmentServer(t, liveShipmentsBody, nil)
	defer server.Close()

	got, err := clientFor(server).ListShipments(
		context.Background(), "shop.myshopify.com", "tok", time.Time{}, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d parcels, want 2", len(got))
	}
	if got[0].Waybill != "90000000001" || got[1].Waybill != "BD123" {
		t.Errorf("waybills %q, %q", got[0].Waybill, got[1].Waybill)
	}
	// The order's details ride along on both.
	for _, parcel := range got {
		if parcel.OrderName != "TEST-00001" || parcel.OrderID != 10000000000001 {
			t.Errorf("order lost: %+v", parcel)
		}
		if parcel.CustomerName != "Test Buyer" || parcel.City != "Testpur" {
			t.Errorf("address lost: %+v", parcel)
		}
		// email was null; the order's own phone wins over the address's.
		if parcel.Phone != "+910000000001" || parcel.Email != "" {
			t.Errorf("contact wrong: phone=%q email=%q", parcel.Phone, parcel.Email)
		}
		if len(parcel.LineItems) != 1 || parcel.LineItems[0].SKU != "DNP-BLU" {
			t.Errorf("line items lost: %+v", parcel.LineItems)
		}
		if parcel.AdminURL != "https://shop.myshopify.com/admin/orders/10000000000001" {
			t.Errorf("admin url %q", parcel.AdminURL)
		}
	}
	if got[0].ShippedAt == nil || got[0].ShippedAt.Format(time.RFC3339) != "2026-10-08T10:02:45Z" {
		t.Errorf("shipped at %v", got[0].ShippedAt)
	}
}

// A Delhivery key knows nothing about a Blue Dart waybill, so tracking one
// would cost a request to learn nothing. The filter is the carrier name
// Shopify recorded, matched loosely because stores write it differently.
func TestListShipmentsFiltersByCarrier(t *testing.T) {
	server := shipmentServer(t, liveShipmentsBody, nil)
	defer server.Close()

	got, err := clientFor(server).ListShipments(
		context.Background(), "shop.myshopify.com", "tok", time.Time{}, "delhivery")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Waybill != "90000000001" {
		t.Fatalf("got %+v, want only the Delhivery parcel", got)
	}
	if got[0].Carrier != "Delhivery" {
		t.Errorf("carrier %q", got[0].Carrier)
	}
}

// Only fulfilled orders, and only within the window: an unfulfilled order has
// no waybill, and the rest of the store's history is not this page's business.
func TestListShipmentsAsksShopifyForShippedOrdersOnly(t *testing.T) {
	var vars map[string]any
	server := shipmentServer(t, `{"data":{"orders":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}`, &vars)
	defer server.Close()

	since := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if _, err := clientFor(server).ListShipments(
		context.Background(), "shop.myshopify.com", "tok", since, ""); err != nil {
		t.Fatalf("list: %v", err)
	}
	query, _ := vars["query"].(string)
	if !strings.Contains(query, "fulfillment_status:shipped") {
		t.Errorf("query %q is missing the fulfilment filter", query)
	}
	if !strings.Contains(query, "created_at:>=2026-09-20") {
		t.Errorf("query %q is missing the window", query)
	}
}

// A fulfilment with no tracking number is a parcel nobody can trace. It is
// skipped rather than listed with a blank waybill, which would be tracked as
// the empty string and come back as a carrier error.
func TestListShipmentsSkipsFulfilmentsWithoutATrackingNumber(t *testing.T) {
	body := `{"data":{"orders":{"pageInfo":{"hasNextPage":false},"nodes":[{
      "id":"gid://shopify/Order/1","name":"T-1","createdAt":"2026-10-08T07:41:26Z",
      "totalPriceSet":{"shopMoney":{"amount":"1.0","currencyCode":"INR"}},
      "lineItems":{"nodes":[]},
      "fulfillments":[{"createdAt":"2026-10-08T10:02:45Z","displayStatus":"IN_TRANSIT","trackingInfo":[{"company":"Delhivery","number":"   ","url":""}]}]
    }]}}}`
	server := shipmentServer(t, body, nil)
	defer server.Close()

	got, err := clientFor(server).ListShipments(
		context.Background(), "shop.myshopify.com", "tok", time.Time{}, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
