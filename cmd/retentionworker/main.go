// Command retentionworker removes finished work older than the shop keeps,
// and leaves a summary of each day behind.
//
// ITS OWN TICKER, NOT A RIVER PERIODIC JOB. River runs periodic jobs on the
// elected leader alone, and the lease on this database is held by whichever
// of several clients took it first - which is how the Shopify order sync came
// to tick six times in forty-six hours while looking perfectly configured.
// This process ticks for itself.
//
// OFF UNLESS ASKED, TWICE. RETENTION_ENABLED is false by default, and
// RETENTION_DRY_RUN is true by default, so the first thing a deployment does
// is report what it WOULD delete. Turning both over is two deliberate acts,
// which is the least this deserves: every other switch in this system changes
// what Tensor does, and this one changes what Tensor still has.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/retention"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// tick is deliberately slow. A retention sweep is a once-a-day job with no
// deadline, and waking often would only mean more chances to delete something
// during an incident nobody has finished diagnosing.
const tick = 6 * time.Hour

const stopTimeout = 30 * time.Second

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New(cfg.LogLevel)

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{MaxConns: 4})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// SAID AT STARTUP, not at the first sweep. A deployment that believes it
	// is pruning and is not should be obvious on the first page of the logs,
	// and so should one that is about to delete for real.
	logger.Info("retention worker running",
		"enabled", cfg.RetentionEnabled,
		"dry_run", cfg.RetentionDryRun,
		"keep_days", cfg.RetentionDays,
		"tick", tick.String())
	if !cfg.RetentionEnabled {
		logger.Info("retention is switched off, so nothing will be deleted or summarised")
	} else if cfg.RetentionDryRun {
		logger.Warn("retention is in DRY RUN: days will be summarised and nothing deleted")
	} else {
		logger.Warn("retention will DELETE finished work older than the retention period")
	}

	var objects retention.ObjectStore
	if client, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	}); err != nil {
		// Not fatal, and not silent. Rows can still be removed; the models
		// behind them would simply stay in the bucket, which costs money
		// rather than correctness.
		logger.Warn("object storage is unavailable, so stored models will not be removed",
			"error", err)
	} else {
		objects = client
	}

	runOnce(ctx, store, objects, cfg, logger)

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()
			<-shutdown.Done()
			logger.Info("retention worker stopped")
			return
		case <-ticker.C:
			runOnce(ctx, store, objects, cfg, logger)
		}
	}
}

func runOnce(
	ctx context.Context, store *db.Store, objects retention.ObjectStore,
	cfg config.Settings, logger *slog.Logger,
) {
	if !cfg.RetentionEnabled {
		return
	}

	brands, err := store.Q.ListBrands(ctx)
	if err != nil {
		logger.Error("could not list brands for the retention sweep", "error", err)
		return
	}
	slugs := make([]string, 0, len(brands))
	for _, b := range brands {
		slugs = append(slugs, b.Slug)
	}
	if len(slugs) == 0 {
		return
	}

	results, err := retention.Sweep(ctx, store, objects, slugs, retention.Options{
		Days:   cfg.RetentionDays,
		DryRun: cfg.RetentionDryRun,
		Zone:   httpapi.ReportZone(),
	})
	// Results are logged even on error: a sweep that failed on its fourth day
	// still did the first three, and the counts are the only record of that.
	for _, r := range results {
		logger.Info("retention swept a day",
			"brand", r.BrandSlug,
			"day_ending", r.WindowEnd.Format(time.RFC3339),
			"dry_run", r.DryRun,
			"orders", r.Orders, "jobs", r.Jobs, "batches", r.Batches,
			"deleted_orders", r.DeletedOrders, "deleted_jobs", r.DeletedJobs,
			"deleted_batches", r.DeletedBatches, "files_removed", r.FilesRemoved)
	}
	if err != nil {
		logger.Error("the retention sweep stopped early", "error", err, "days_done", len(results))
		return
	}
	if len(results) == 0 {
		logger.Debug("nothing is old enough to remove")
	}
}
