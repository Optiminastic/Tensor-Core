package httpapi

// The sync endpoints must hand the pull to a worker, never run it on the
// request.
//
// This is the regression these tests exist for: the import used to run inside
// the HTTP handler, the browser abandons that request after five seconds, and
// Gin cancels the context when it does. Every order still to be written failed -
// silently, because per-order failures are logged and skipped by design - so the
// endpoint answered 200 {"imported": 4} while thirty-five orders in the middle
// of the range never arrived. Orders 114775-114809 were missing from the Orders
// page for exactly that reason.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// fakeOrderSyncEnqueuer records what was scheduled instead of scheduling it.
type fakeOrderSyncEnqueuer struct {
	brands []string
	err    error
}

func (f *fakeOrderSyncEnqueuer) Enqueue(_ context.Context, brandSlug string) error {
	if f.err != nil {
		return f.err
	}
	f.brands = append(f.brands, brandSlug)
	return nil
}

// seedConnectedShopify gives a brand a usable Shopify connection.
func seedConnectedShopify(t *testing.T, store *db.Store, slug string) {
	t.Helper()
	token, domain := "shpat_test", testShopDomain
	if _, err := store.Q.UpsertConnection(context.Background(), gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: slug, Provider: "shopify", Status: "connected",
		ExternalAccountID: &domain, AccessToken: &token,
	}); err != nil {
		t.Fatalf("seed connected connection: %v", err)
	}
}

// syncTestRouter builds a router whose order sync is the fake above.
func syncTestRouter(t *testing.T, store *db.Store, guards *auth.Guards, e OrderSyncEnqueuer) http.Handler {
	t.Helper()
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		CORSOrigins: []string{"http://localhost:3001"},
	}
	s := NewServer(cfg, store, guards, nil)
	s.EnableOrderSync(e)
	return s.Router()
}

// A connected brand's sync is scheduled, not performed. If the handler still
// imported inline it would reach for the (absent) Shopify client and fail here
// rather than answering 202.
func TestIntegrationSyncShopifyOrdersSchedulesTheImport(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	manage := minter.mint(t, []string{"brand:manage"})
	seedConnectedShopify(t, store, "gifting")

	fake := &fakeOrderSyncEnqueuer{}
	router := syncTestRouter(t, store, auth.NewGuards(minter.verifier, ""), fake)

	rr := doJSON(router, http.MethodPost, "/brands/gifting/connections/shopify/sync", manage, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("sync = %d body=%s, want 202 - the pull belongs on a worker, "+
			"not on a request the browser abandons after five seconds", rr.Code, rr.Body.String())
	}
	var body struct {
		Started  bool `json:"started"`
		Imported *int `json:"imported"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.Started {
		t.Error(`response omits "started": true`)
	}
	if body.Imported != nil {
		t.Error(`response still reports "imported"; nothing has been imported yet, ` +
			"and a count here could only be a guess")
	}
	if len(fake.brands) != 1 || fake.brands[0] != "gifting" {
		t.Errorf("scheduled %v, want one pull for gifting", fake.brands)
	}
}

// The all-brands view schedules one pull covering every store. The empty slug
// is the worker's "all brands" - a real slug there would sync a brand called
// "all", which does not exist.
func TestIntegrationSyncAllShopifyOrdersSchedulesOnePullForEveryStore(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	manage := minter.mint(t, []string{"brand:manage"})
	seedConnectedShopify(t, store, "gifting")

	fake := &fakeOrderSyncEnqueuer{}
	router := syncTestRouter(t, store, auth.NewGuards(minter.verifier, ""), fake)

	rr := doJSON(router, http.MethodPost, "/connections/shopify/sync-all", manage, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("sync-all = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if len(fake.brands) != 1 || fake.brands[0] != "" {
		t.Errorf("scheduled %v, want a single all-brands pull (empty slug)", fake.brands)
	}
}

// With no worker attached the endpoint says so. Reporting success for a pull
// nothing will ever run is the failure mode this whole change is about.
func TestIntegrationSyncShopifyOrdersSaysSoWhenNothingCanRunIt(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	manage := minter.mint(t, []string{"brand:manage"})
	seedConnectedShopify(t, store, "gifting")

	router := syncTestRouter(t, store, auth.NewGuards(minter.verifier, ""), nil)
	rr := doJSON(router, http.MethodPost, "/brands/gifting/connections/shopify/sync", manage, nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("sync with no enqueuer = %d, want 503", rr.Code)
	}
}
