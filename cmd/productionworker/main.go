// Command productionworker consumes the River jobs that make the production
// pipeline run without an operator: it turns each imported Shopify order into
// production jobs, and replans batches when new work becomes batchable.
//
// It is deliberately a separate process from cmd/sliceworker. The two have very
// different resource profiles and failure modes - a stuck Bambu Studio
// subprocess holding a slice slot must never starve order intake.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

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

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Storage is best-effort here, unlike in the slice worker. Job creation only
	// reads an object's size when minting a design's template file asset, and
	// falls back to zero without it - not a reason to refuse to process orders.
	var objects *storage.Client
	if objects, err = storage.New(ctx, storage.Config{
		Endpoint:   cfg.MinIOEndpoint,
		Region:     cfg.MinIORegion,
		AccessKey:  cfg.MinIOAccessKey,
		SecretKey:  cfg.MinIOSecretKey,
		Bucket:     cfg.MinIOBucket,
		Prefix:     cfg.MinIOPrefix,
		Secure:     cfg.MinIOSecure,
		AutoCreate: cfg.MinIOAutoCreate,
	}); err != nil {
		log.Printf("object storage unavailable, continuing without it: %v", err)
		objects = nil
	}

	// This process never serves HTTP (Router() is never called), so a nil
	// verifier is safe: NewGuards is a cheap, side-effect-free constructor and no
	// guard is ever exercised here.
	guards := auth.NewGuards(nil, "")
	server := httpapi.NewServer(cfg, store, guards, logger)
	server.EnablePipeline(objects, nil)

	concurrency := cfg.ProductionConcurrency
	if concurrency < 1 {
		concurrency = 1
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, httpapi.NewJobCreationWorker(server, logger))
	river.AddWorker(workers, httpapi.NewBatchPlanWorker(server, logger))
	river.AddWorker(workers, httpapi.NewPrintDispatchWorker(server, logger))
	river.AddWorker(workers, httpapi.NewSyncDispatchesWorker(server, logger))

	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			// Two queues so a burst of order webhooks can never starve batch
			// planning, or the reverse.
			cfg.Queue(production.JobCreationQueueName): {MaxWorkers: concurrency},
			cfg.Queue(production.BatchPlanQueueName):   {MaxWorkers: 1},
			// Dispatch is I/O-bound on the link to the shop LAN. One at a time:
			// prints are physical events, and serialising them keeps the order
			// they reach a printer predictable.
			cfg.Queue(production.DispatchQueueName): {MaxWorkers: 1},
		},
		Workers: workers,
		Logger:  logger,
		// A safety net, not the primary path: batchable jobs are normally
		// replanned by an event trigger the moment they appear. This tick means
		// that even if every trigger were lost, work still gets reconsidered.
		//
		// RunOnStart is false so a redeploy does not itself force a replan, and
		// the tick shares Enqueue's debounce so a tick landing next to an event
		// trigger collapses into one run rather than firing twice.
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(cfg.BatchPlanInterval),
				production.PeriodicBatchPlanConstructor(cfg.BatchPlanDebounce, cfg.Queue(production.BatchPlanQueueName)),
				&river.PeriodicJobOpts{RunOnStart: false},
			),
			// Print status poll. RunOnStart is true here, unlike the batch replan:
			// after a restart the first thing worth knowing is whether a print that
			// was running has since finished or failed.
			river.NewPeriodicJob(
				river.PeriodicInterval(cfg.BambuddyPollInterval),
				production.PeriodicDispatchSyncConstructor(cfg.BambuddyPollInterval, cfg.Queue(production.DispatchQueueName)),
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}

	// The worker enqueues as well as consumes: creating an order's jobs makes new
	// work batchable, which schedules a debounced replan. Without this the worker
	// would depend entirely on the periodic tick to notice its own output.
	server.EnableProductionPipeline(
		production.NewJobCreationEnqueuer(client, cfg.Queue(production.JobCreationQueueName)),
		production.NewBatchPlanEnqueuer(client, cfg.BatchPlanDebounce, cfg.Queue(production.BatchPlanQueueName)),
		production.NewDispatchEnqueuer(client, cfg.Queue(production.DispatchQueueName)),
	)

	if err := client.Start(ctx); err != nil {
		log.Fatalf("start river: %v", err)
	}
	log.Printf("production worker running (concurrency=%d, replan debounce=%s, tick=%s)",
		concurrency, cfg.BatchPlanDebounce, cfg.BatchPlanInterval)
	if cfg.BambuddyConfigured() {
		log.Printf("print dispatch + status poll enabled (bambuddy=%s, poll=%s, staged=%t)",
			cfg.BambuddyBaseURL, cfg.BambuddyPollInterval, cfg.BambuddyManualStart)
	} else {
		log.Print("print dispatch disabled: BAMBUDDY_BASE_URL/BAMBUDDY_API_KEY not set")
	}

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
