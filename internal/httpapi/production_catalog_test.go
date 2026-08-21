package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// importSkuOrder imports a paid order with one line item carrying the given SKU
// (and no material property, so a catalog hit is what fills it), returning the
// imported order's id.
func importSkuOrder(t *testing.T, router http.Handler, store *db.Store, shopifyOrderID int, sku string) uuid.UUID {
	t.Helper()
	// The order number must be distinct per order: migration 0013 added a unique
	// index on (shop_connection_id, order_number), and a real store never reuses
	// one. A fixed "#T" made the second import collide on that index, which the
	// upsert's ON CONFLICT (shopify_order_id) cannot absorb.
	payload := map[string]any{
		"id": shopifyOrderID, "name": fmt.Sprintf("#T%d", shopifyOrderID),
		"financial_status": "paid", "total_price": "999.00", "currency": "INR",
		"customer": map[string]any{"first_name": "Ada", "last_name": "L"},
		"line_items": []map[string]any{
			{"product_id": 900, "sku": sku, "name": "Moon Lamp", "quantity": 1},
		},
	}
	body, _ := json.Marshal(payload)
	headers := map[string]string{"X-Shopify-Hmac-SHA256": signWebhook(body), "X-Shopify-Shop-Domain": testShopDomain}
	if rr := doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid", headers, body); rr.Code != http.StatusOK {
		t.Fatalf("import order %d = %d body=%s", shopifyOrderID, rr.Code, rr.Body.String())
	}
	orders, err := store.Q.ListOrders(context.Background())
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	for _, o := range orders {
		if o.ShopifyOrderID == int64(shopifyOrderID) {
			return o.ID
		}
	}
	t.Fatalf("imported order %d not found", shopifyOrderID)
	return uuid.Nil
}

// TestIntegrationOrderSkuResolvesToDesign proves the SKU catalog "brain": an
// order whose SKU matches a design produces a job with the design's model
// attached (print_file_id) and material filled from the catalog, while a
// non-catalogue SKU is left on the line-item fallback.
func TestIntegrationOrderSkuResolvesToDesign(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := shopifyTestServer(t, store, guards)
	ctx := context.Background()
	brand := string(pricing.BrandGifting)

	seedConnection(t, store, testShopDomain)

	// A catalogued design with a unique SKU (material PLA, from seedPricedDesign).
	designID := seedPricedDesign(t, store, brand, verdictGreen)
	sku := "ML-WS-001"
	if _, err := store.Q.SetDesignSku(ctx, gen.SetDesignSkuParams{ID: designID, Sku: &sku}); err != nil {
		t.Fatalf("set sku: %v", err)
	}

	// A matching order becomes a job with the design's STL + material attached.
	orderID := importSkuOrder(t, router, store, 70001, sku)
	jobs := fromOrderJobs(t, router, minter, orderID)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	job, err := store.Q.GetProductionJobByID(ctx, uuid.MustParse(jobs[0].ID))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PrintFileID == nil {
		t.Errorf("print_file_id not attached from the catalog")
	}
	if job.Material == nil || *job.Material != "PLA" {
		t.Errorf("material = %v, want PLA from the design", job.Material)
	}
	if job.Sku == nil || *job.Sku != sku {
		t.Errorf("job sku = %v, want %s", job.Sku, sku)
	}

	// The design now caches its template file, so reprints reuse the same asset.
	d, err := store.Q.GetDesignBySku(ctx, &sku)
	if err != nil || d.TemplateFileID == nil {
		t.Fatalf("template file not cached on the design (err %v)", err)
	}

	// A non-catalogue SKU leaves the job on the line-item fallback: no print file.
	orderID2 := importSkuOrder(t, router, store, 70002, "NOT-A-SKU-999")
	jobs2 := fromOrderJobs(t, router, minter, orderID2)
	if len(jobs2) != 1 {
		t.Fatalf("jobs2 = %d, want 1", len(jobs2))
	}
	job2, err := store.Q.GetProductionJobByID(ctx, uuid.MustParse(jobs2[0].ID))
	if err != nil {
		t.Fatalf("get job2: %v", err)
	}
	if job2.PrintFileID != nil {
		t.Errorf("unmatched SKU should not attach a print file")
	}
}

// TestIntegrationDesignSkuUniqueAndGuarded covers the SKU write endpoint: it needs
// shopify:publish, and a duplicate SKU is a 409.
func TestIntegrationDesignSkuUniqueAndGuarded(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	brand := string(pricing.BrandGifting)

	a := seedPricedDesign(t, store, brand, verdictGreen)
	b := seedPricedDesign(t, store, brand, verdictGreen)

	// Without shopify:publish -> 403.
	reader := minter.mint(t, []string{"design:read"})
	if rr := doJSON(router, http.MethodPatch, "/designs/"+a.String()+"/sku", reader,
		map[string]any{"sku": "SKU-1"}); rr.Code != http.StatusForbidden {
		t.Errorf("set sku without permission = %d, want 403", rr.Code)
	}

	publisher := minter.mint(t, []string{"shopify:publish"})
	if rr := doJSON(router, http.MethodPatch, "/designs/"+a.String()+"/sku", publisher,
		map[string]any{"sku": "SKU-1"}); rr.Code != http.StatusOK {
		t.Fatalf("set sku = %d body=%s", rr.Code, rr.Body.String())
	}
	// The same SKU on another design -> 409.
	if rr := doJSON(router, http.MethodPatch, "/designs/"+b.String()+"/sku", publisher,
		map[string]any{"sku": "SKU-1"}); rr.Code != http.StatusConflict {
		t.Errorf("duplicate sku = %d, want 409", rr.Code)
	}
}
