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
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
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
	if cfg.FakeSlice {
		log.Println("FAKE_SLICE is enabled: every slice will be fabricated, not run through Bambu Studio")
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, slicing.NewSliceWorker(
		store, objects, cfg.BambuRoot, sliceTimeout, cfg.PrinterAvgPowerKW, orientOpts, logger, cfg.FakeSlice,
	))
	// Same queue, same Bambu install, different question: SliceArgs costs one
	// design, SliceBatchArgs measures one merged bed (see batch_queue.go).
	river.AddWorker(workers, slicing.NewBatchSliceWorker(
		store, objects, cfg.BambuRoot, sliceTimeout, logger, cfg.FakeSlice,
	))

	// Third kind: slice a bed for printing with Tensor's own profile and hand
	// the file to BambuBuddy (print_worker.go). It enqueues the queueing step on
	// another queue, through an insert-only client - this one knows only the
	// slice kinds.
	insertOnly, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build insert-only river client: %v", err)
	}
	processes, err := slicing.LoadSystemPresets(cfg.BambuRoot, "process")
	if err != nil {
		// Profiles still slice; only the gaps their parents would fill fall
		// back to the CLI's defaults. Loud, because that is a different slice.
		log.Printf("bundled process presets unreadable, print slices will not fill from parents: %v", err)
	}
	river.AddWorker(workers, slicing.NewPrintSliceWorker(slicing.PrintSliceDeps{
		Store:     store.Q,
		Objects:   objects,
		Library:   bambubuddy.NewWithTimeout(cfg.BambuBuddyURL, cfg.BambuBuddyAPIKey, time.Duration(cfg.BambuBuddyTimeoutSeconds)*time.Second),
		Queuer:    production.NewQueueSlicedPlateEnqueuer(insertOnly),
		Processes: processes,
		BambuRoot: cfg.BambuRoot,
		Timeout:   sliceTimeout,
		Logger:    logger,
	}))

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
