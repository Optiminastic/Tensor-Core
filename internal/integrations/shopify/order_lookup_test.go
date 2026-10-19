package shopify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type gqlCall struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// scripted answers each GraphQL call with the next canned response.
func scripted(t *testing.T, responses ...string) (*Client, *[]gqlCall) {
	t.Helper()
	var calls []gqlCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var c gqlCall
		_ = json.Unmarshal(raw, &c)
		calls = append(calls, c)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responses[len(calls)-1])
	}))
	t.Cleanup(server.Close)
	return testClient(server.URL), &calls
}

const orderByNameResponse = `{"data":{"orders":{"nodes":[{
  "id":"gid://shopify/Order/1","name":"T3DPS-115766","createdAt":"2026-10-12T10:33:59Z","cancelledAt":null,
  "phone":null,"shippingLine":{"title":"EXPRESS DISPATCH"},"displayFulfillmentStatus":"FULFILLED",
  "shippingAddress":{"phone":"+91 98765 43210"},"billingAddress":null,
  "lineItems":{"nodes":[{"customAttributes":[{"key":"STEP 6 - WhatsApp Number","value":"99999 88888"}]}]},
  "fulfillments":[
    {"createdAt":"2026-10-13T10:00:00Z","displayStatus":"IN_TRANSIT","trackingInfo":[{"company":"Delhivery","number":"2","url":"u2"}]},
    {"createdAt":"2026-10-12T10:00:00Z","displayStatus":"DELIVERED","trackingInfo":[{"company":"Delhivery","number":"1","url":"u1"}]}
  ]}]}}}`

func TestOrderByName(t *testing.T) {
	c, calls := scripted(t, orderByNameResponse)
	o, found, err := c.OrderByName(context.Background(), "shop", "tok", "T3DPS-115766")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if q := (*calls)[0].Variables["q"]; q != "name:T3DPS-115766" {
		t.Fatalf("search = %v", q)
	}
	if o.ID != "gid://shopify/Order/1" || o.ShippingTitle != "EXPRESS DISPATCH" || o.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("order = %+v", o)
	}
	if o.Fulfillments[0].DisplayStatus != "DELIVERED" || o.Fulfillments[1].Tracking[0].Number != "2" {
		t.Fatalf("fulfillments must be oldest first: %+v", o.Fulfillments)
	}
	if len(o.Phones) != 1 || o.Phones[0] != "+91 98765 43210" {
		t.Fatalf("phones = %v", o.Phones)
	}
	if len(o.Attributes) != 1 || o.Attributes[0].Value != "99999 88888" {
		t.Fatalf("attributes = %v", o.Attributes)
	}
}

func TestOrderByNameNeedsAnExactName(t *testing.T) {
	for _, resp := range []string{
		`{"data":{"orders":{"nodes":[]}}}`,
		// Shopify's name search is fuzzy; a different order must not count.
		`{"data":{"orders":{"nodes":[{"id":"gid://shopify/Order/2","name":"T3DPS-1157660","createdAt":"2026-10-12T10:33:59Z"}]}}}`,
	} {
		c, _ := scripted(t, resp)
		_, found, err := c.OrderByName(context.Background(), "shop", "tok", "T3DPS-115766")
		if err != nil || found {
			t.Fatalf("found=%v err=%v for %s", found, err, resp)
		}
	}
}

func TestAddOrderTags(t *testing.T) {
	c, calls := scripted(t, `{"data":{"tagsAdd":{"userErrors":[]}}}`)
	if err := c.AddOrderTags(context.Background(), "shop", "tok", "gid://shopify/Order/1", "wa-confirmed"); err != nil {
		t.Fatal(err)
	}
	v := (*calls)[0].Variables
	if v["id"] != "gid://shopify/Order/1" || v["tags"].([]any)[0] != "wa-confirmed" {
		t.Fatalf("variables = %v", v)
	}
}

func TestAddOrderTagsSurfacesUserErrors(t *testing.T) {
	c, _ := scripted(t, `{"data":{"tagsAdd":{"userErrors":[{"field":["id"],"message":"Order does not exist"}]}}}`)
	err := c.AddOrderTags(context.Background(), "shop", "tok", "gid://shopify/Order/1", "x")
	if err == nil || !strings.Contains(err.Error(), "Order does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppendOrderNoteKeepsTheExistingNote(t *testing.T) {
	c, calls := scripted(t,
		`{"data":{"order":{"note":"Gift wrap please"}}}`,
		`{"data":{"orderUpdate":{"userErrors":[]}}}`,
	)
	if err := c.AppendOrderNote(context.Background(), "shop", "tok", "gid://shopify/Order/1", "WhatsApp damage report: 2 photos."); err != nil {
		t.Fatal(err)
	}
	input := (*calls)[1].Variables["input"].(map[string]any)
	if input["note"] != "Gift wrap please\nWhatsApp damage report: 2 photos." {
		t.Fatalf("note = %q", input["note"])
	}
}

// The order lookup must not ask for anything needing a scope this app does
// not hold.
//
// `customer` needs read_customers, and Shopify fails the WHOLE query over one
// unreadable field - so asking for it turned every lookup into "Access denied
// for customer field" and the bot could not answer anybody about any order.
// Caught only by running it against the live shop, which is why it is pinned
// here.
func TestTheLookupAsksForNothingNeedingReadCustomers(t *testing.T) {
	if strings.Contains(orderByNameQuery, "customer") {
		t.Error("orderByNameQuery asks for a customer field; that needs read_customers " +
			"and fails the entire query. Take the phone from shippingAddress instead.")
	}
	// The sources that remain, and that the ownership check depends on.
	for _, field := range []string{"shippingAddress", "billingAddress", "phone"} {
		if !strings.Contains(orderByNameQuery, field) {
			t.Errorf("orderByNameQuery no longer asks for %s", field)
		}
	}
}
