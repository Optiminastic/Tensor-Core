// Command productionworker consumes River production-pipeline jobs: today,
// Job Creation (Stage 2 + Stage 3 - see internal/production/queue.go). It is a
// separate process from cmd/sliceworker so a stuck Bambu Studio subprocess can
// never starve job/batch processing, and shares the same domain code as cmd/api
// by building a full *httpapi.Server (unstarted - Router() is never called).
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
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const stopTimeout = 30 * time.Second

// periodicJobs assembles the scheduled work this process owns.
//
// The two entries differ in RunOnStart, and deliberately so. A replan on every
// deploy would be wasted work against an unchanged backlog. A fleet refresh on
// every deploy is the opposite: a fresh deployment's machines table is empty
// until something populates it, which is exactly the "Machine Management shows
// nothing" state operators hit. Refreshing is cheap and idempotent, and
// UniqueOpts bounds a restart loop.
func periodicJobs(cfg config.Settings, debounce, fleetSync time.Duration) []*river.PeriodicJob {
	jobs := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(time.Duration(cfg.BatchPlanIntervalMinutes)*time.Minute),
			production.PeriodicBatchPlanConstructor(debounce),
			&river.PeriodicJobOpts{RunOnStart: false},
		),
	}
	if fleetSync <= 0 {
		return jobs // FLEET_SYNC_INTERVAL_SECONDS=0 opts out entirely
	}
	return append(jobs, river.NewPeriodicJob(
		river.PeriodicInterval(fleetSync),
		production.PeriodicFleetSyncConstructor(fleetSync),
		&river.PeriodicJobOpts{RunOnStart: true},
	))
}

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New("info")

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

	objects, err := storage.New(ctx, storage.Options{
		Endpoint:           cfg.S3Endpoint,
		AccessKey:          cfg.S3AccessKey,
		SecretKey:          cfg.S3SecretKey,
		Bucket:             cfg.S3Bucket,
		KeyPrefix:          cfg.S3KeyPrefix,
		Secure:             cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage unavailable: %v", err)
	}

	// This process never serves HTTP requests (Router() is never called), so a
	// nil verifier is safe - NewGuards is a cheap, side-effect-free constructor
	// and nothing here ever exercises a guard.
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

	fleetSyncInterval := time.Duration(cfg.FleetSyncIntervalSeconds) * time.Second
	if fleetSyncInterval > 0 {
		river.AddWorker(workers, httpapi.NewFleetSyncWorker(
			server, logger, time.Duration(cfg.FleetSyncTimeoutMinutes)*time.Minute,
		))
	}

	debounce := time.Duration(cfg.BatchPlanDebounceSeconds) * time.Second
	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			production.JobCreationQueueName: {MaxWorkers: concurrency},
			production.BatchPlanQueueName:   {MaxWorkers: 1},
			// Its own queue, and one at a time: a refresh waits on a printer
			// host that may be asleep, and must never hold the single
			// batch-plan slot while it does.
			production.FleetSyncQueueName: {MaxWorkers: 1},
		},
		Workers: workers,
		Logger:  logger,
		// Replans periodically regardless of activity - the backstop that
		// makes the threshold-gated per-job triggers (see
		// triggerBatchPlanIfThresholdMet) safe to skip below the threshold.
		// RunOnStart is deliberately false: a worker restart/redeploy
		// shouldn't itself force an immediate replan. Shares Enqueue's
		// debounce so a tick landing close to an event trigger collapses
		// into one run instead of firing twice.
		PeriodicJobs: periodicJobs(cfg, debounce, fleetSyncInterval),
	})
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}

	// The workers hold *Server by pointer, so attaching the enqueuers after the
	// client exists but before Start is what makes triggerBatchPlanIfThresholdMet
	// real in this process. Without it s.batchEnqueuer stays nil, the trigger
	// hits its nil guard and silently returns, and job creation here never
	// prompts a replan - leaving the periodic tick as the only path, up to
	// BatchPlanIntervalMinutes late.
	//
	// A consuming client can Insert as well as Work, so there is no need for a
	// second insert-only client. Nor is there a double-enqueue hazard: Enqueue
	// and the periodic job share UniqueOpts{ByPeriod: debounce}, which dedupes
	// in the database across every process.
	server.EnableProductionQueue(
		production.NewJobCreationEnqueuer(client),
		production.NewBatchPlanEnqueuer(client, debounce),
	)
	// The slice enqueuer, for the same reason - this process is where batches
	// are created, and creating one is what queues the slice of its merged
	// plate (see enqueuePlateSlice). Without it every batch silently keeps
	// batchTimeFromJobs' MAX-of-jobs approximation instead of a measurement of
	// its own bed.
	//
	// This one DOES need its own insert-only client, unlike the two above. A
	// consuming client validates every inserted job against its Workers
	// bundle, and this process deliberately registers no slice workers (it has
	// no Bambu Studio) - so enqueuing through `client` fails at runtime with
	// "job kind is not registered in the client's Workers bundle: slice_batch".
	// An insert-only client has no bundle and no such check.
	insertOnly, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build insert-only river client: %v", err)
	}
	server.EnableSliceEnqueuer(slicing.NewEnqueuer(insertOnly))

	if err := client.Start(ctx); err != nil {
		log.Fatalf("start river: %v", err)
	}
	log.Printf("production worker running (concurrency=%d)", concurrency)

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
