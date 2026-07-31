package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

const testShopifySecret = "test-app-secret"
const testShopDomain = "acme.myshopify.com"

// shopifyTestServer builds a server with the inbound Shopify config populated.
func shopifyTestServer(t *testing.T, store *db.Store, guards *auth.Guards) http.Handler {
	t.Helper()
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core", CORSOrigins: []string{"http://localhost:3001"},
		ShopifyClientID: "client-id", ShopifyClientSecret: testShopifySecret,
		PublicBaseURL: "https://tensor.example", FrontendURL: "https://app.example",
		TokenEncryptionKey: "test-encryption-key",
	}
	return NewServer(cfg, store, guards, nil).Router()
}

// doRaw sends a raw body (needed so the webhook HMAC matches the bytes exactly).
func doRaw(router http.Handler, method, path string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func seedConnection(t *testing.T, store *db.Store, domain string) uuid.UUID {
	t.Helper()
	conn, err := store.Q.InsertShopifyConnection(context.Background(), gen.InsertShopifyConnectionParams{
		ID: uuid.New(), ShopDomain: domain, EncryptedAccessToken: "sealed",
	})
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	return conn.ID
}

func signWebhook(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testShopifySecret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestIntegrationOrdersPaidWebhook(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := shopifyTestServer(t, store, guards)

	seedConnection(t, store, testShopDomain)

	payload := map[string]any{
		"id": 55501, "name": "#1042", "financial_status": "paid", "total_price": "1499.00", "currency": "INR",
		"customer": map[string]any{"first_name": "Ada", "last_name": "Lovelace"},
		"line_items": []map[string]any{
			{
				"product_id": 900, "name": "Custom Nameplate", "quantity": 1,
				"properties": []map[string]any{
					{"name": "material", "value": "PLA"},
					{"name": "personalisation_name", "value": "Ada"},
					{"name": "personalisation_font", "value": "Serif"},
					{"name": "personalisation_colour", "value": "Gold"},
					{"name": "personalisation_variant", "value": "Large"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	// Bad signature -> 401.
	if rr := doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid",
		map[string]string{"X-Shopify-Hmac-SHA256": "bad", "X-Shopify-Shop-Domain": testShopDomain}, body); rr.Code != http.StatusUnauthorized {
		t.Errorf("bad-hmac webhook = %d, want 401", rr.Code)
	}
	// Unknown store -> 404.
	if rr := doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid",
		map[string]string{"X-Shopify-Hmac-SHA256": signWebhook(body), "X-Shopify-Shop-Domain": "nope.myshopify.com"}, body); rr.Code != http.StatusNotFound {
		t.Errorf("unknown-store webhook = %d, want 404", rr.Code)
	}
	// Valid -> 200 and the order is imported.
	headers := map[string]string{"X-Shopify-Hmac-SHA256": signWebhook(body), "X-Shopify-Shop-Domain": testShopDomain}
	if rr := doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid", headers, body); rr.Code != http.StatusOK {
		t.Fatalf("webhook = %d body=%s", rr.Code, rr.Body.String())
	}
	// Replay is idempotent (upsert on shopify_order_id).
	if rr := doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid", headers, body); rr.Code != http.StatusOK {
		t.Fatalf("webhook replay = %d", rr.Code)
	}

	// The imported order feeds from-order: one job with the mapped material and a
	// validated personalisation (name supplied, nothing else required).
	orders, err := store.Q.ListOrders(context.Background())
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders after webhook = %d (err %v), want 1", len(orders), err)
	}
	jobs := fromOrderJobs(t, router, minter, orders[0].ID)
	if len(jobs) != 1 {
		t.Fatalf("from-order jobs = %d, want 1", len(jobs))
	}
	if jobs[0].PersonalisationStatus != "validated" {
		t.Errorf("personalisation = %q, want validated", jobs[0].PersonalisationStatus)
	}
}

func TestIntegrationShopifyConnectGuards(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := shopifyTestServer(t, store, guards)

	// Listing connections needs integration:manage.
	if rr := doJSON(router, http.MethodGet, "/integrations/shopify", minter.mint(t, []string{"order:read"}), nil); rr.Code != http.StatusForbidden {
		t.Errorf("list connections without permission = %d, want 403", rr.Code)
	}
	manage := minter.mint(t, []string{"integration:manage"})
	if rr := doJSON(router, http.MethodGet, "/integrations/shopify", manage, nil); rr.Code != http.StatusOK {
		t.Errorf("list connections = %d, want 200", rr.Code)
	}

	// Authorize builds a redirect to Shopify.
	rr := doJSON(router, http.MethodGet, "/integrations/shopify/authorize?shop_domain="+testShopDomain, manage, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("authorize = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc == "" || loc[:8] != "https://" {
		t.Errorf("authorize redirect = %q, want a Shopify URL", loc)
	}
}
