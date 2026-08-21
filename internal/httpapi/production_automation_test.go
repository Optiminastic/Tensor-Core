package httpapi

// Integration tests for the automatic half of the production pipeline: an
// imported Shopify order schedules its own job creation, and doing it twice is
// harmless. These are the guarantees the job-creation worker relies on.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// automationTestServer is shopifyTestServer plus the production pipeline wired
// up, so the webhook actually enqueues instead of no-oping. It returns the
// Server too, since the worker-facing tests call its methods directly rather
// than going through HTTP.
func automationTestServer(t *testing.T, store *db.Store, guards *auth.Guards) (*Server, http.Handler) {
	t.Helper()
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core", CORSOrigins: []string{"http://localhost:3001"},
		ShopifyClientID: "client-id", ShopifyClientSecret: testShopifySecret,
		PublicBaseURL: "https://tensor.example", FrontendURL: "https://app.example",
		TokenEncryptionKey: "test-encryption-key",
	}
	server := NewServer(cfg, store, guards, nil)

	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		t.Fatalf("river insert-only client: %v", err)
	}
	server.EnableProductionPipeline(
		production.NewJobCreationEnqueuer(client, production.JobCreationQueueName),
		production.NewBatchPlanEnqueuer(client, 0, production.BatchPlanQueueName),
		production.NewDispatchEnqueuer(client, production.DispatchQueueName),
	)
	return server, server.Router()
}

// countRiverJobs counts queued River jobs of one kind. The worker itself is not
// running in these tests, so a row here means "this work was scheduled".
func countRiverJobs(t *testing.T, store *db.Store, kind string) int {
	t.Helper()
	var n int
	err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&n)
	if err != nil {
		t.Fatalf("count river jobs: %v", err)
	}
	return n
}

func paidOrderBody(t *testing.T, shopifyOrderID int, orderNumber, sku string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": shopifyOrderID, "name": orderNumber, "financial_status": "paid",
		"total_price": "999.00", "currency": "INR",
		"customer": map[string]any{"first_name": "Ada", "last_name": "L"},
		"line_items": []map[string]any{
			{"product_id": 900, "sku": sku, "name": "Moon Lamp", "quantity": 1},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

func postPaidOrder(t *testing.T, router http.Handler, body []byte) int {
	t.Helper()
	return doRaw(router, http.MethodPost, "/webhooks/shopify/orders-paid", map[string]string{
		"X-Shopify-Hmac-SHA256": signWebhook(body),
		"X-Shopify-Shop-Domain": testShopDomain,
	}, body).Code
}

// An imported order must schedule its own job creation. Before this, an order
// landed in the table and waited for someone to press a button.
func TestIntegrationOrdersPaidEnqueuesJobCreation(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	_, router := automationTestServer(t, store, guards)
	seedConnection(t, store, testShopDomain)

	if got := postPaidOrder(t, router, paidOrderBody(t, 80001, "#A1", "ANY-SKU")); got != http.StatusOK {
		t.Fatalf("import = %d, want 200", got)
	}

	orders, err := store.Q.ListOrders(context.Background())
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(orders))
	}
	if n := countRiverJobs(t, store, "create_jobs_from_order"); n != 1 {
		t.Errorf("scheduled job-creation jobs = %d, want 1", n)
	}
}

// The order upsert and its scheduled job creation share one transaction, so a
// replay must not produce a second order. A second scheduled job is fine and
// expected - the worker collapses it (see the idempotency test below).
func TestIntegrationOrdersPaidReplayCreatesNoSecondOrder(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	_, router := automationTestServer(t, store, guards)
	seedConnection(t, store, testShopDomain)

	body := paidOrderBody(t, 80002, "#A2", "ANY-SKU")
	if got := postPaidOrder(t, router, body); got != http.StatusOK {
		t.Fatalf("first import = %d, want 200", got)
	}
	if got := postPaidOrder(t, router, body); got != http.StatusOK {
		t.Fatalf("replayed import = %d, want 200", got)
	}

	orders, err := store.Q.ListOrders(context.Background())
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 {
		t.Errorf("orders after replay = %d, want 1 (upsert is keyed on shopify_order_id)", len(orders))
	}
}

// The worker treats errJobsAlreadyCreated as success rather than retrying, so
// this sentinel is load-bearing: it is what stops a redelivered webhook, or an
// operator racing the worker, from duplicating an order's jobs.
func TestIntegrationCreateJobsForOrderIsIdempotent(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	server, router := automationTestServer(t, store, guards)
	seedConnection(t, store, testShopDomain)

	if got := postPaidOrder(t, router, paidOrderBody(t, 80003, "#A3", "ANY-SKU")); got != http.StatusOK {
		t.Fatalf("import = %d, want 200", got)
	}
	ctx := context.Background()
	orders, err := store.Q.ListOrders(ctx)
	if err != nil || len(orders) != 1 {
		t.Fatalf("list orders = %d (err %v), want 1", len(orders), err)
	}
	orderID := orders[0].ID

	created, err := server.CreateJobsForOrder(ctx, orderID)
	if err != nil {
		t.Fatalf("first CreateJobsForOrder: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}

	// Second run: the sentinel, not a duplicate and not a generic error.
	_, err = server.CreateJobsForOrder(ctx, orderID)
	if !errors.Is(err, errJobsAlreadyCreated) {
		t.Fatalf("second CreateJobsForOrder err = %v, want errJobsAlreadyCreated", err)
	}

	// And the HTTP route still renders that sentinel as its documented 409.
	var he *httpErr
	if !errors.As(err, &he) || he.status != http.StatusConflict {
		t.Fatalf("sentinel does not carry a 409 (err %v)", err)
	}
	if he.msg != "Production jobs have already been created for this order." {
		t.Errorf("conflict detail = %q, changed from the documented string", he.msg)
	}

	count, err := store.Q.CountJobsForOrder(ctx, &orderID)
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Errorf("jobs for order = %d, want 1", count)
	}
}

// A missing order must not look like "already created" - the worker would then
// silently swallow it as success and the order would never produce jobs.
func TestIntegrationCreateJobsForUnknownOrderIs404(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	server, _ := automationTestServer(t, store, guards)

	_, err := server.CreateJobsForOrder(context.Background(), uuid.New())
	if errors.Is(err, errJobsAlreadyCreated) {
		t.Fatal("unknown order reported as already-created")
	}
	var he *httpErr
	if !errors.As(err, &he) || he.status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 httpErr", err)
	}
}
