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

	objects, err := storage.New(
		ctx, cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOBucket, cfg.MinIOSecure,
	)
	if err != nil {
		log.Fatalf("object storage unavailable: %v", err)
	}

	concurrency := cfg.SliceConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	sliceTimeout := time.Duration(cfg.SliceTimeoutSeconds) * time.Second

	orientOpts := orientation.Options{
		OverhangThresholdDeg: cfg.OrientationOverhangDeg,
		MaxTriangles:         cfg.OrientationMaxTriangles,
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, slicing.NewSliceWorker(
		store, objects, cfg.BambuRoot, sliceTimeout, cfg.PrinterAvgPowerKW, orientOpts, logger,
	))

	client, err := river.NewClient(riverpgxv5.New(store.Pool), &river.Config{
		Queues:  map[string]river.QueueConfig{slicing.QueueName: {MaxWorkers: concurrency}},
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
