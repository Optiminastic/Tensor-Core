// Command replate rebuilds every bed's merged plate from its jobs' current
// models.
//
// A merged plate is a snapshot taken once, when the bed was formed. Re-rendering
// the jobs behind it changes nothing about the file already stored, so after a
// template, font or colour fix every bed still holds - and would still print -
// the old geometry. cmd/rerender fixes the models; this is what carries that fix
// onto the plates.
//
// Run it AFTER a rerender has finished. Running it while renders are still in
// flight would merge whatever models happen to exist at that moment.
//
// Completed beds are never touched: their plate is the record of what was
// actually printed.
//
//	go run ./cmd/replate -dry-run
//	go run ./cmd/replate
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report which beds would be re-plated without touching them")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New(cfg.LogLevel)
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Object storage is not optional here: a plate is read from and written
	// back to it, so without one there is nothing this command can do.
	objects, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}

	// Never serves HTTP, so a nil verifier is safe - the same reasoning as
	// cmd/productionworker. The nil enqueuer means a re-plate does not queue a
	// re-slice; BambuBuddy slices the plate it is sent.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)
	server.EnablePipeline(objects, nil)

	beds, err := server.PlateableBatches(ctx)
	if err != nil {
		log.Fatalf("list beds: %v", err)
	}
	fmt.Printf("beds to re-plate: %d\n", len(beds))
	if len(beds) == 0 {
		return
	}
	if *dryRun {
		for _, b := range beds {
			fmt.Println("  ", b.BatchNumber, b.Status)
		}
		fmt.Println("\ndry run: nothing was re-plated")
		return
	}

	done, failed := 0, 0
	for _, b := range beds {
		if err := server.RebuildBatchPlate(ctx, b.ID); err != nil {
			fmt.Printf("  FAILED %s: %v\n", b.BatchNumber, err)
			failed++
			continue
		}
		done++
	}
	fmt.Printf("\nre-plated %d bed(s), %d failed\n", done, failed)
	if failed > 0 {
		os.Exit(1)
	}
}
