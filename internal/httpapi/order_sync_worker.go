package httpapi

// The River worker that pulls a brand's Shopify orders.
//
// This exists because the sync used to run inside the HTTP request that asked
// for it, on c.Request.Context(). Importing a few hundred orders is minutes of
// work - a round trip to Shopify per page and a database write per order - and
// the browser gives up long before that. When it did, the request context was
// cancelled and every remaining import failed.
//
// It failed SILENTLY, which is what made it costly: per-order failures are
// logged and skipped by design, so the endpoint returned {"imported": 4} and
// looked like a success. The store had orders 114775-114814; Tensor had four of
// them, and the gap grew every day.
//
// On the worker there is no HTTP deadline, so the pull runs to completion.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// OrderSyncWorker imports a brand's orders from Shopify.
type OrderSyncWorker struct {
	river.WorkerDefaults[production.SyncOrdersArgs]

	server  *Server
	logger  *slog.Logger
	timeout time.Duration
}

// NewOrderSyncWorker builds the worker. logger may be nil (falls back to
// slog's default); timeout 0 keeps River's one-minute default.
func NewOrderSyncWorker(server *Server, logger *slog.Logger, timeout time.Duration) *OrderSyncWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &OrderSyncWorker{server: server, logger: logger, timeout: timeout}
}

// Timeout overrides River's one-minute default. A pull is up to eight paged
// GraphQL calls and then a database write per order; a minute is not enough for
// a backlog, and cutting the import off part way is the exact failure this
// worker exists to end.
func (w *OrderSyncWorker) Timeout(*river.Job[production.SyncOrdersArgs]) time.Duration {
	return w.timeout
}

// Work runs one sync.
//
// An empty BrandSlug means every connected store, which is what the periodic
// tick and the Orders page's "All brands" view both ask for.
//
// Tokens are resolved here rather than carried in the job args: a River job is
// a database row that outlives the request, and a Shopify access token has no
// business being stored in one.
func (w *OrderSyncWorker) Work(ctx context.Context, job *river.Job[production.SyncOrdersArgs]) error {
	if strings.TrimSpace(job.Args.BrandSlug) == "" {
		return w.syncAll(ctx, job.Attempt)
	}
	return w.syncBrand(ctx, job.Args.BrandSlug, job.Attempt)
}

// syncBrand pulls one store.
//
// An error is returned so River retries - a sync that failed on Shopify's side
// is exactly the case worth trying again, unlike the dispatch pass where a
// retry would re-upload plates that already landed. Importing is idempotent
// (orders upsert on their Shopify id), so a retry costs time, not duplicates.
func (w *OrderSyncWorker) syncBrand(ctx context.Context, slug string, attempt int) error {
	conn, err := w.server.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: slug, Provider: shopifyProvider,
	})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		// Not worth retrying: the brand has no Shopify connection, and no
		// amount of retrying will give it one.
		w.logger.Warn("order sync skipped, brand has no Shopify connection", "brand", slug)
		return nil
	}

	w.logger.Info("shopify order sync start", "brand", slug, "shop", shop, "attempt", attempt)
	imported, err := w.server.SyncOrdersForBrand(ctx, shop, token)
	if err != nil {
		return fmt.Errorf("sync orders for %s: %w", slug, err)
	}
	w.logger.Info("shopify order sync complete", "brand", slug, "imported", imported)
	return nil
}

// syncAll pulls every connected store.
//
// One store failing does not abandon the others - brands are independent, and
// stopping at the first bad token would leave the rest stale for no reason
// anybody can see. The error returned at the end names how many failed, so
// River retries the pass while the stores that did work keep their imports.
func (w *OrderSyncWorker) syncAll(ctx context.Context, attempt int) error {
	conns, err := w.server.store.Q.ListConnectedShopifyBrands(ctx)
	if err != nil {
		return fmt.Errorf("list connected shopify stores: %w", err)
	}
	if len(conns) == 0 {
		w.logger.Info("order sync: no brand has Shopify connected")
		return nil
	}

	imported, failed := 0, 0
	for _, conn := range conns {
		if conn.ExternalAccountID == nil || conn.AccessToken == nil {
			continue
		}
		n, err := w.server.SyncOrdersForBrand(ctx, *conn.ExternalAccountID, *conn.AccessToken)
		if err != nil {
			w.logger.Error("shopify order sync failed for a brand",
				"brand", conn.BrandSlug, "shop", *conn.ExternalAccountID, "error", err)
			failed++
			continue
		}
		imported += n
	}
	w.logger.Info("shopify order sync complete",
		"brands", len(conns), "imported", imported, "failed", failed, "attempt", attempt)
	if failed > 0 {
		return fmt.Errorf("%d of %d stores could not be synced", failed, len(conns))
	}
	return nil
}

// OrderSyncEnqueuer schedules order syncs. Wraps the same insert-only River
// client the other enqueuers use.
type OrderSyncEnqueuer interface {
	Enqueue(ctx context.Context, brandSlug string) error
}

// EnableOrderSync attaches the enqueuer, so the sync endpoint can hand work to
// the worker instead of doing it on the request.
func (s *Server) EnableOrderSync(e OrderSyncEnqueuer) { s.orderSyncEnqueuer = e }
