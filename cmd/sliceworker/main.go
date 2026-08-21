// Command sliceworker consumes River "slice" jobs: it slices each uploaded design
// with Bambu Studio and prices it. It replaces the Python Celery worker and its
// FastAPI dispatcher - Go enqueues to River directly, so no broker (Redis) and no
// HTTP callback are involved. It shares the same domain code as cmd/api.
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

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
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

	objects, err := storage.New(ctx, storage.Config{
		Endpoint:   cfg.MinIOEndpoint,
		Region:     cfg.MinIORegion,
		AccessKey:  cfg.MinIOAccessKey,
		SecretKey:  cfg.MinIOSecretKey,
		Bucket:     cfg.MinIOBucket,
		Prefix:     cfg.MinIOPrefix,
		Secure:     cfg.MinIOSecure,
		AutoCreate: cfg.MinIOAutoCreate,
	})
	if err != nil {
		log.Fatalf("object storage unavailable: %v", err)
	}

	concurrency := cfg.SliceConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	// A non-positive timeout would make context.WithTimeout expire immediately and
	// fail every slice; clamp it to the default.
	timeoutSeconds := cfg.SliceTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	sliceTimeout := time.Duration(timeoutSeconds) * time.Second

	orientOpts := orientation.Options{
		OverhangThresholdDeg: cfg.OrientationOverhangDeg,
		MaxTriangles:         cfg.OrientationMaxTriangles,
	}
	// The plate worker schedules the print once a plate exists. It needs an
	// insert-only River client for that, built before the consuming client below.
	dispatchClient, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build river insert client: %v", err)
	}
	plateWorker := slicing.NewPlateSliceWorker(store, objects, cfg.BambuRoot, sliceTimeout, logger)
	if cfg.BambuddyConfigured() {
		plateWorker.EnableDispatch(production.NewDispatchEnqueuer(dispatchClient, cfg.Queue(production.DispatchQueueName)))
		log.Printf("plate dispatch enabled (bambuddy=%s)", cfg.BambuddyBaseURL)
	} else {
		log.Print("plate dispatch disabled: BAMBUDDY_BASE_URL/BAMBUDDY_API_KEY not set")
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, slicing.NewSliceWorker(
		store, objects, cfg.BambuRoot, sliceTimeout, cfg.PrinterAvgPowerKW, orientOpts, logger,
	))
	// Batch plates slice in this same process: both kinds drive Bambu Studio, and
	// one process owning it keeps the concurrency cap meaningful.
	river.AddWorker(workers, plateWorker)

	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues:  map[string]river.QueueConfig{cfg.Queue(slicing.QueueName): {MaxWorkers: concurrency}},
		Workers: workers,
		Logger:  logger,
	})
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		log.Fatalf("start river: %v", err)
	}
	log.Printf("slice worker running (bambu=%s, concurrency=%d, timeout=%s)",
		cfg.BambuRoot, concurrency, sliceTimeout)

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
