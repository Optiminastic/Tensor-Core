// Command personalisationworker consumes the CUSTOMER'S queues and nothing
// else: the thirty-minute link, the preview the customer is editing, and the
// sweep that closes the window and releases the order.
//
// WHY IT IS ITS OWN PROCESS. The floor's work and the customer's work have
// different deadlines. A plate render, a Shopify pull or a batch plan can run
// late and cost nothing but patience; a window that closes late is an order
// that misses its bed, and an order held for a customer who was never asked is
// invisible on every screen. Behind one process the floor's backlog sits in
// front of the customer's timer. Measured on 2026-10-27: 31 queued jobs
// excluded from every planning run and no new batch for hours.
//
// THE PIPELINE STILL RUNS THESE QUEUES BY DEFAULT, so deploying this changes
// nothing on its own - River locks each job to one worker, so both processes
// may consume them safely. Set RUN_PERSONALISATION_WORKERS=false on the
// pipeline once this is up, and only then. Doing it the other way round stops
// links being sent and stops them expiring, which is the outage this exists
// to prevent.
//
// NO PERIODIC JOBS. River elects one leader across every client on the
// database and runs periodic jobs there; if this process won that election it
// would swallow the floor's replan and dispatch ticks without consuming either
// queue. workerset.StartPersonalisation registers none.
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
		// Fatal, as in cmd/productionworker: every preview this process
		// renders is written to object storage, so starting without it would
		// fail one customer at a time and read as a modelling problem rather
		// than a deployment one.
		log.Fatalf("object storage unavailable: %v", err)
	}

	// Never serves HTTP (Router() is never called), so a nil verifier is safe:
	// NewGuards is a cheap constructor and no guard is ever exercised here.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)
	server.EnablePipeline(objects, nil)

	runner, err := workerset.StartPersonalisation(ctx, server, store, cfg, logger)
	if err != nil {
		log.Fatalf("start personalisation workers: %v", err)
	}
	logger.Info("personalisation worker running",
		"window_minutes", cfg.PersonalisationLinkMinutes,
		"check_enabled", cfg.PersonalisationCheckEnabled)

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := runner.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
