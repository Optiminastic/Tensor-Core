package httpapi

// The whole win-back schedule, end to end, as the worker runs it.
//
// The real sweep, the real ledger and the real River queue, against a fake
// Shopify and a fake Sarvam - so every rule that decides who is rung runs
// exactly as it does in tensor-abandoned-checkout-worker, and nobody's phone
// does. Time is moved by rewriting the ledger, not by waiting.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const fakeShop = "winback-e2e.myshopify.com"

// placedCallRecord is one call the fake Sarvam was asked to place.
type placedCallRecord struct {
	Phone   string
	Version int
}

// fakeSarvam accepts every call and remembers who and which version.
type fakeSarvam struct {
	mu    sync.Mutex
	calls []placedCallRecord
}

func (f *fakeSarvam) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AppConfig struct {
			AppVersion int `json:"app_version"`
		} `json:"app_config"`
		UserConfig struct {
			UserPhoneNumber string `json:"user_phone_number"`
		} `json:"user_config"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, placedCallRecord{body.UserConfig.UserPhoneNumber, body.AppConfig.AppVersion})
	n := len(f.calls)
	f.mu.Unlock()
	_, _ = fmt.Fprintf(w, `{"attempt_id":"att-%d"}`, n)
}

func (f *fakeSarvam) since(n int) []placedCallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]placedCallRecord(nil), f.calls[n:]...)
}

// fakeCheckout is what the fake Shopify lists.
type fakeCheckout struct {
	Name, Phone string
	Created     time.Time
	Completed   *time.Time
}

type fakeShopify struct {
	mu        sync.Mutex
	checkouts []fakeCheckout
}

func (f *fakeShopify) set(checkouts ...fakeCheckout) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkouts = checkouts
}

func (f *fakeShopify) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	nodes := make([]map[string]any, 0, len(f.checkouts))
	for _, c := range f.checkouts {
		node := map[string]any{
			"id": "gid://shopify/AbandonedCheckout/" + c.Name, "name": c.Name,
			"createdAt":            c.Created.UTC().Format(time.RFC3339),
			"abandonedCheckoutUrl": "https://" + fakeShop + "/recover/" + c.Name,
			"lineItemsQuantity":    1,
			"totalPriceSet": map[string]any{
				"shopMoney": map[string]any{"amount": "599.0", "currencyCode": "INR"},
			},
			"shippingAddress": map[string]any{
				"firstName": "Test", "lastName": c.Name, "phone": c.Phone,
			},
			"lineItems": map[string]any{"nodes": []map[string]any{
				{"title": "Dual Name Plank", "quantity": 1},
			}},
		}
		if c.Completed != nil {
			node["completedAt"] = c.Completed.UTC().Format(time.RFC3339)
		}
		nodes = append(nodes, node)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"abandonedCheckouts": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false},
			"nodes":    nodes,
		},
	}})
}

// routeShopTo sends every connection for the fake shop's host to the given
// TLS server. Shopify's client always dials https://<shop>, and this is the
// only seam that reaches it without changing production code.
func routeShopTo(t *testing.T, shop *httptest.Server) {
	t.Helper()
	original := http.DefaultTransport
	target := shop.Listener.Addr().String()
	dialer := &net.Dialer{}
	http.DefaultTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == fakeShop+":443" {
				addr = target
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only fake
	}
	t.Cleanup(func() { http.DefaultTransport = original })
}

// e2eWorld is everything one run of the schedule needs.
type e2eWorld struct {
	store  *db.Store
	sarvam *fakeSarvam
	shop   *fakeShopify
	river  *river.Client[pgx.Tx]
	done   <-chan *river.Event
}

func newE2EWorld(t *testing.T, callsEnabled bool) *e2eWorld {
	t.Helper()
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx,
		`TRUNCATE abandoned_checkout_calls, winback_watermarks, brand_connections`); err != nil {
		t.Fatalf("truncate win-back tables: %v", err)
	}

	sarvamFake := &fakeSarvam{}
	sarvamServer := httptest.NewServer(sarvamFake)
	t.Cleanup(sarvamServer.Close)
	shopFake := &fakeShopify{}
	shopServer := httptest.NewTLSServer(shopFake)
	t.Cleanup(shopServer.Close)
	routeShopTo(t, shopServer) // before NewServer: the Shopify client clones the transport

	cfg := config.Settings{
		ShopifyAPIVersion: "2025-01", ShopifyTimeout: 10 * time.Second,
		SarvamBaseURL: sarvamServer.URL, SarvamAPIKey: "sk_test", SarvamOrgID: "org",
		SarvamWorkspaceID: "ws", SarvamAgentID: "agent", SarvamAgentVersion: 6,
		SarvamConnectionID: "conn", SarvamAgentPhoneNumber: "+918000000000",
		WinbackCallsEnabled: callsEnabled, WinbackCallDelayMinutes: 0, WinbackCallMaxAgeHours: 24,
		WinbackCallMaxPerRun: 5, WinbackCallAgentVersion: 8,
		WinbackFollowUpAgentVersion: 9, WinbackFollowUpDelayMinutes: 20,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(cfg, store, auth.NewGuards(nil, ""), logger)

	slug := string(pricing.BrandGifting)
	shop, token := fakeShop, "shpat_test"
	if _, err := store.Q.UpsertConnection(ctx, gen.UpsertConnectionParams{
		ID: uuid.New(), BrandSlug: slug, Provider: shopifyProvider, Status: "connected",
		ExternalAccountID: &shop, AccessToken: &token,
	}); err != nil {
		t.Fatalf("connect the fake shop: %v", err)
	}
	// Watching began an hour ago, so the carts below are all new ones.
	if _, err := store.Q.EnsureWinbackWatermark(ctx, slug); err != nil {
		t.Fatalf("watch mark: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`UPDATE winback_watermarks SET watch_from = now() - interval '1 hour'`); err != nil {
		t.Fatalf("backdate the watch mark: %v", err)
	}

	// The same River wiring cmd/winbackworker uses: one queue, one at a time.
	workers := river.NewWorkers()
	river.AddWorker(workers, NewWinbackCallWorker(server, logger))
	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Logger:  logger,
		Workers: workers,
		Queues:  map[string]river.QueueConfig{production.WinbackCallQueueName: {MaxWorkers: 1}},
	})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	done, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	t.Cleanup(cancel)
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start river: %v", err)
	}
	t.Cleanup(func() { _ = client.Stop(context.Background()) })

	return &e2eWorld{store: store, sarvam: sarvamFake, shop: shopFake, river: client, done: done}
}

// sweep enqueues one sweep exactly as the worker's ticker does and waits for it.
func (w *e2eWorld) sweep(t *testing.T) {
	t.Helper()
	res, err := w.river.Insert(context.Background(), production.WinbackCallsArgs{}, nil)
	if err != nil {
		t.Fatalf("enqueue the sweep: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		t.Fatal("the sweep was refused as a duplicate")
	}
	select {
	case ev := <-w.done:
		if ev.Kind != river.EventKindJobCompleted {
			t.Fatalf("the sweep did not complete: %s", ev.Kind)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep never finished")
	}
}

// backdateCall moves this number's first call into the past.
func (w *e2eWorld) backdateCall(t *testing.T, phone string, ago time.Duration) {
	t.Helper()
	tag, err := w.store.Pool.Exec(context.Background(),
		`UPDATE abandoned_checkout_calls SET last_attempt_at = now() - $2::interval
		 WHERE phone = $1 AND channel = 'voice'`,
		phone, fmt.Sprintf("%d seconds", int(ago.Seconds())))
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate %s: %v (rows %d)", phone, err, tag.RowsAffected())
	}
}

func TestIntegrationWinbackCallsAtOnceThenFollowsUpOnce(t *testing.T) {
	w := newE2EWorld(t, true)
	now := time.Now()
	const (
		due     = "+919000000001" // still unpaid at +20: followed up
		expired = "+919000000002" // first call 45 minutes ago: the code is gone
		paid    = "+919000000003" // bought through a fresh checkout
	)
	w.shop.set(
		fakeCheckout{Name: "#1001", Phone: "9000000001", Created: now.Add(-30 * time.Second)},
		fakeCheckout{Name: "#1002", Phone: "9000000002", Created: now.Add(-30 * time.Second)},
		fakeCheckout{Name: "#1003", Phone: "9000000003", Created: now.Add(-30 * time.Second)},
	)

	// T+0: every new cart is rung on the first sweep, on version 8, with no
	// waiting.
	w.sweep(t)
	first := w.sarvam.since(0)
	if len(first) != 3 {
		t.Fatalf("first sweep placed %d calls, want 3: %+v", len(first), first)
	}
	for _, c := range first {
		if c.Version != 8 {
			t.Errorf("first call to %s ran version %d, want 8", c.Phone, c.Version)
		}
	}

	// A second sweep straight after rings nobody.
	w.sweep(t)
	if again := w.sarvam.since(3); len(again) != 0 {
		t.Fatalf("an immediate second sweep rang %+v", again)
	}

	// Nineteen minutes on: too soon for the follow-up.
	w.backdateCall(t, due, 19*time.Minute)
	w.sweep(t)
	if early := w.sarvam.since(3); len(early) != 0 {
		t.Fatalf("followed up at 19 minutes: %+v", early)
	}

	// Twenty-one minutes on for two of them, forty-five for the third; the
	// third customer has meanwhile paid through a new checkout.
	w.backdateCall(t, due, 21*time.Minute)
	w.backdateCall(t, expired, 45*time.Minute)
	w.backdateCall(t, paid, 21*time.Minute)
	completed := now.Add(-time.Minute)
	w.shop.set(
		fakeCheckout{Name: "#1001", Phone: "9000000001", Created: now.Add(-30 * time.Second)},
		fakeCheckout{Name: "#1002", Phone: "9000000002", Created: now.Add(-30 * time.Second)},
		fakeCheckout{Name: "#1003", Phone: "9000000003", Created: now.Add(-30 * time.Second)},
		fakeCheckout{Name: "#1004", Phone: "9000000003", Created: now.Add(-10 * time.Second),
			Completed: &completed},
	)
	w.sweep(t)
	follow := w.sarvam.since(3)
	if len(follow) != 1 || follow[0].Phone != due || follow[0].Version != 9 {
		t.Fatalf("follow-ups = %+v, want exactly one, to %s on version 9", follow, due)
	}

	// And only once.
	w.sweep(t)
	if again := w.sarvam.since(4); len(again) != 0 {
		t.Fatalf("followed up twice: %+v", again)
	}

	// The ledger holds both calls for the followed-up customer, each placed.
	rows, err := w.store.Pool.Query(context.Background(),
		`SELECT channel, status FROM abandoned_checkout_calls WHERE phone = $1 ORDER BY channel`, due)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var channel, status string
		_ = rows.Scan(&channel, &status)
		got = append(got, channel+"="+status)
	}
	if fmt.Sprint(got) != "[voice=placed voice_followup=placed]" {
		t.Errorf("ledger for %s = %v", due, got)
	}
}

// With calling switched off, the follow-up is as silent as the first call.
func TestIntegrationWinbackDisabledRingsNobody(t *testing.T) {
	w := newE2EWorld(t, false)
	w.shop.set(
		fakeCheckout{Name: "#2001", Phone: "9000000008", Created: time.Now()},
		fakeCheckout{Name: "#2002", Phone: "9000000009", Created: time.Now()},
	)
	// #2002 as if its first call had gone out 21 minutes ago: due a follow-up
	// by every rule but the switch.
	if _, err := w.store.Pool.Exec(context.Background(),
		`INSERT INTO abandoned_checkout_calls
		   (id, brand_slug, checkout_id, checkout_name, phone, channel, status, last_attempt_at)
		 VALUES ($1, $2, 'gid', '#2002', '+919000000009', 'voice', 'placed',
		         now() - interval '21 minutes')`,
		uuid.New(), string(pricing.BrandGifting)); err != nil {
		t.Fatalf("seed a first call: %v", err)
	}

	w.sweep(t)
	if calls := w.sarvam.since(0); len(calls) != 0 {
		t.Fatalf("calling is off and %d calls were placed: %+v", len(calls), calls)
	}
	var rows int
	if err := w.store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM abandoned_checkout_calls`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("ledger has %d rows (%v), want only the seeded one", rows, err)
	}
}
