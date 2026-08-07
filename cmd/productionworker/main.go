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
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const stopTimeout = 30 * time.Second

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

	debounce := time.Duration(cfg.BatchPlanDebounceSeconds) * time.Second
	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			production.JobCreationQueueName: {MaxWorkers: concurrency},
			production.BatchPlanQueueName:   {MaxWorkers: 1},
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
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(time.Duration(cfg.BatchPlanIntervalMinutes)*time.Minute),
				production.PeriodicBatchPlanConstructor(debounce),
				&river.PeriodicJobOpts{RunOnStart: false},
			),
		},
	})
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}

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
