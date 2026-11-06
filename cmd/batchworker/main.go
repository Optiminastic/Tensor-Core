// Command batchworker runs the BED'S whole lifecycle and nothing else.
//
//	plan      jobs of one product and one colour, that fit one bed, become a
//	          Draft; the planner dissolves and reforms Drafts as work arrives
//	lock      a Draft with enough units and a free printer of its class
//	          becomes Locked: plate merged, filament reserved, machine stamped
//	dispatch  a Locked bed is sliced and sent to its printer (printing)
//	pin       a sent plate is tied to the printer somebody chose
//	complete  the fleet sync notices the print finished and completes the bed,
//	          releasing its jobs to Assembly
//
// WHY IT IS ITS OWN PROCESS. It is one state machine, and the pipeline's other
// work - job creation, OpenSCAD renders, the Shopify pull - is bursty and long.
// Behind one process a ready bed waits on a backlog of renders it has nothing
// to do with. Here it waits on nothing but the printers.
//
// THE PIPELINE STILL RUNS THESE QUEUES BY DEFAULT, so deploying this changes
// nothing on its own - River locks each job to one worker, so both processes
// may consume them safely. Set RUN_BATCH_WORKERS=false on the pipeline (and on
// the API, if it runs the pipeline itself) once this is up, and only then.
// Doing it the other way round stops every bed forming and every print
// completing, with nothing on screen to say why.
//
// NO RIVER PERIODIC JOBS. The replan, dispatch and fleet ticks are tickers this
// process owns (workerset.startBatchTickers), so they fire whichever client
// holds River's leader lease.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/storage"
	"github.com/Optiminastic/tensor-core/internal/workerset"
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
		// Fatal, as in cmd/productionworker: locking a bed merges its models
		// into one plate read from and written to object storage, so starting
		// without it would fail one bed at a time and read as a modelling
		// problem rather than a deployment one.
		log.Fatalf("object storage unavailable: %v", err)
	}

	// Never serves HTTP (Router() is never called), so a nil verifier is safe:
	// NewGuards is a cheap constructor and no guard is ever exercised here.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)
	server.EnablePipeline(objects, nil)

	runner, err := workerset.StartBatch(ctx, server, store, cfg, logger)
	if err != nil {
		log.Fatalf("start batch workers: %v", err)
	}
	logger.Info("batch worker running",
		"plan_minutes", cfg.BatchPlanIntervalMinutes,
		"auto_dispatch", cfg.BatchAutoDispatch,
		"fleet_sync_seconds", cfg.FleetSyncIntervalSeconds)

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := runner.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
