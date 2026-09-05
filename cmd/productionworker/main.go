// Command productionworker consumes River production-pipeline jobs: job
// creation, model rendering, batch planning, dispatch and the Shopify order
// pull (see internal/production/queue.go).
//
// Since internal/workerset exists, cmd/api runs the same set in-process by
// default, so this command is no longer required for the pipeline to run. It
// stays for deployments that want the pipeline on its own host - away from the
// API's request traffic, or on the machine that actually has OpenSCAD - and for
// running the workers alone while the API is being restarted. Both processes
// consume the same queues safely: River locks each job to one worker.
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
		// Fatal here, unlike in cmd/api: every job this process runs reads or
		// writes a model file, so a pipeline without object storage would fail
		// one job at a time and look like a data problem instead of a
		// deployment one.
		log.Fatalf("object storage unavailable: %v", err)
	}

	// This process never serves HTTP requests (Router() is never called), so a
	// nil verifier is safe - NewGuards is a cheap, side-effect-free constructor
	// and nothing here ever exercises a guard.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)
	server.EnablePipeline(objects, nil)

	runner, err := workerset.Start(ctx, server, store, cfg, logger)
	if err != nil {
		log.Fatalf("start production workers: %v", err)
	}

	<-ctx.Done()
	log.Println("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := runner.Stop(stopCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
