package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

const testShopifySecret = "test-app-secret"
const testShopDomain = "acme.myshopify.com"

// newShopifyTestServer builds a server with the inbound Shopify config
// populated. The *Server itself is returned alongside its router because the
// order-import path is exercised in-process (there is no inbound HTTP
// endpoint for it - orders are only ever pulled from Shopify's API).
func newShopifyTestServer(t *testing.T, store *db.Store, guards *auth.Guards) (*Server, http.Handler) {
	t.Helper()
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core", CORSOrigins: []string{"http://localhost:3001"},
		ShopifyClientID: "client-id", ShopifyClientSecret: testShopifySecret,
		PublicBaseURL: "https://tensor.example", FrontendURL: "https://app.example",
		TokenEncryptionKey: "test-encryption-key",
	}
	srv := NewServer(cfg, store, guards, nil)
	return srv, srv.Router()
}

// shopifyTestServer is newShopifyTestServer for callers that only need the router.
func shopifyTestServer(t *testing.T, store *db.Store, guards *auth.Guards) http.Handler {
	t.Helper()
	_, router := newShopifyTestServer(t, store, guards)
	return router
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

// TestIntegrationImportShopifyOrder covers the single path every order takes
// into Tensor: a pull from Shopify's API (the "Sync from Shopify" button and
// the post-connect catch-up both land here via fetchAndImportShopifyOrders).
// It asserts the upsert is idempotent across repeated syncs and that the
// imported order feeds job creation with its personalisation mapped.
func TestIntegrationImportShopifyOrder(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	srv, router := newShopifyTestServer(t, store, guards)

	connID := seedConnection(t, store, testShopDomain)

	productID := int64(900)
	payload := shopifyOrderPayload{
		ID: 55501, Name: "#1042", FinancialStatus: "paid", TotalPrice: "1499.00", Currency: "INR",
		Customer: &shopifyCustomer{FirstName: "Ada", LastName: "Lovelace"},
		LineItems: []shopifyLineItem{{
			ProductID: &productID, Name: "Custom Nameplate", Quantity: 1,
			Properties: []shopifyLineProp{
				{Name: "material", Value: "PLA"},
				{Name: "personalisation_name", Value: "Ada"},
				{Name: "personalisation_font", Value: "Serif"},
				{Name: "personalisation_colour", Value: "Gold"},
				{Name: "personalisation_variant", Value: "Large"},
			},
		}},
	}

	ctx := context.Background()
	if _, err := srv.importShopifyOrder(ctx, &connID, payload, "pending"); err != nil {
		t.Fatalf("import: %v", err)
	}
	// A second sync re-sees the same order: upsert on shopify_order_id, no duplicate.
	if _, err := srv.importShopifyOrder(ctx, &connID, payload, "pending"); err != nil {
		t.Fatalf("re-import: %v", err)
	}

	// The imported order feeds from-order: one job with the mapped material and a
	// validated personalisation (name supplied, nothing else required).
	orders, err := store.Q.ListOrders(ctx, nil)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders after import = %d (err %v), want 1", len(orders), err)
	}
	jobs := fromOrderJobs(t, router, minter, orders[0].ID)
	if len(jobs) != 1 {
		t.Fatalf("from-order jobs = %d, want 1", len(jobs))
	}
	if jobs[0].PersonalisationStatus != "validated" {
		t.Errorf("personalisation = %q, want validated", jobs[0].PersonalisationStatus)
	}
}

// TestIntegrationImportShopifyOrderViaBrandConnection covers a store connected
// only through a brand's Shopify connection (brand_connections), never through
// the separate shopify_connections OAuth grant - its sync has no
// shopify_connections row to reference, so the import runs with a nil connID.
func TestIntegrationImportShopifyOrderViaBrandConnection(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	srv, _ := newShopifyTestServer(t, store, guards)

	domain := "brand-only.myshopify.com"
	token := "shpat_test"
	if _, err := store.Q.UpsertConnection(context.Background(), gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: "gifting", Provider: "shopify", Status: "connected",
		ExternalAccountID: &domain, AccessToken: &token,
	}); err != nil {
		t.Fatalf("seed brand connection: %v", err)
	}

	productID := int64(901)
	payload := shopifyOrderPayload{
		ID: 55502, Name: "#1043", FinancialStatus: "paid", TotalPrice: "260.00", Currency: "INR",
		LineItems: []shopifyLineItem{{ProductID: &productID, Name: "Keychain", Quantity: 1}},
	}
	if _, err := srv.importShopifyOrder(context.Background(), nil, payload, "pending"); err != nil {
		t.Fatalf("import via brand connection: %v", err)
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
	rr := doJSON(router, http.MethodGet, "/integrations/shopify/authorize?shop_domain="+testShopDomain+"&brand=gifting", manage, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("authorize = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc == "" || loc[:8] != "https://" {
		t.Errorf("authorize redirect = %q, want a Shopify URL", loc)
	}

	// A missing/unknown brand is rejected before any redirect is built.
	rr = doJSON(router, http.MethodGet, "/integrations/shopify/authorize?shop_domain="+testShopDomain, manage, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("authorize without brand = %d, want 422", rr.Code)
	}
	rr = doJSON(router, http.MethodGet, "/integrations/shopify/authorize?shop_domain="+testShopDomain+"&brand=no-such-brand", manage, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("authorize with unknown brand = %d, want 422", rr.Code)
	}
}

// TestIntegrationSyncShopifyOrdersGuard covers the brand_connections-based
// sync's "not connected" guard - the actual Shopify fetch (a connected brand)
// isn't exercised here since it would need a live/mocked Shopify Admin API,
// same gap as the rest of this file's Shopify coverage.
func TestIntegrationSyncShopifyOrdersGuard(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	manage := minter.mint(t, []string{"brand:manage"})

	// No connection at all for this brand.
	if rr := doJSON(router, http.MethodPost, "/brands/gifting/connections/shopify/sync", manage, nil); rr.Code != http.StatusConflict {
		t.Errorf("sync with no connection = %d, want 409", rr.Code)
	}

	// A connection exists but isn't in the connected state.
	token, domain := "shpat_test", testShopDomain
	if _, err := store.Q.UpsertConnection(context.Background(), gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: "gifting", Provider: "shopify", Status: "disconnected",
		ExternalAccountID: &domain, AccessToken: &token,
	}); err != nil {
		t.Fatalf("seed disconnected connection: %v", err)
	}
	if rr := doJSON(router, http.MethodPost, "/brands/gifting/connections/shopify/sync", manage, nil); rr.Code != http.StatusConflict {
		t.Errorf("sync with disconnected connection = %d, want 409", rr.Code)
	}
}
