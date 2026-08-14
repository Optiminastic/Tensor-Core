// Command seedreal imports real model files from a local folder as approved
// designs and generates realistic orders against them, then runs the real
// pipeline (job creation, batching) so the whole order -> job -> batch ->
// machine flow can be exercised with real geometry.
//
// It bypasses nothing. Designs are priced through slicing.ProcessSliceResult -
// the same function the slice worker calls - jobs are created through
// Server.CreateJobsForOrder, and batches through Server.AutoCreateBatches.
// The only thing invented is the orders themselves, which stand in for Shopify.
//
// Safe to re-run: designs are keyed by a SKU derived from the filename, so
// importing the same folder twice reuses the existing products rather than
// duplicating them. Orders are additive; pass -reset to clear previous seed
// orders (and only those) first.
//
//	go run ./cmd/seedreal -dir "C:\path\to\models" -orders 35
package main

import (
	"context"
	"flag"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const (
	brandSlug = "my-store"
	createdBy = "seed-script"
)

func main() {
	dir := flag.String("dir", `C:\Users\optiminastic\Desktop\3mf file`, "folder of .3mf/.stl model files to import")
	orderCount := flag.Int("orders", 35, "how many orders to generate")
	reset := flag.Bool("reset", false, "delete previous seed orders and their jobs first (designs are kept)")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	logger := obs.New("info")
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer store.Close()

	objects, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}

	// This process never serves HTTP requests - a nil verifier/enqueuer is
	// safe, matching cmd/productionworker's exact construction pattern.
	guards := auth.NewGuards(nil, "")
	server := httpapi.NewServer(cfg, store, guards, logger)
	server.EnablePipeline(objects, nil)

	profiles, err := machineProfiles(ctx, store)
	if err != nil {
		log.Fatalf("find machine profiles: %v", err)
	}
	log.Printf("using %d machine profile(s)", len(profiles))

	if *reset {
		if err := resetSeedOrders(ctx, store); err != nil {
			log.Fatalf("reset previous seed orders: %v", err)
		}
		log.Println("cleared previous seed orders and their jobs (designs kept)")
	}

	files, err := modelFiles(*dir)
	if err != nil {
		log.Fatalf("list model files in %s: %v", *dir, err)
	}
	log.Printf("found %d model files in %s", len(files), *dir)

	designs, err := createDesigns(ctx, store, objects, files, profiles)
	if err != nil {
		log.Fatalf("create designs: %v", err)
	}
	log.Printf("%d approved designs available", len(designs))
	if len(designs) == 0 {
		log.Fatal("no usable designs; nothing to order")
	}

	orderIDs, err := seedRealOrders(ctx, store, designs, *orderCount)
	if err != nil {
		log.Fatalf("seed orders: %v", err)
	}
	log.Printf("created %d orders", len(orderIDs))

	totalJobs := 0
	for _, oid := range orderIDs {
		jobs, err := server.CreateJobsForOrder(ctx, oid)
		if err != nil {
			log.Printf("create jobs for order %s: %v", oid, err)
			continue
		}
		totalJobs += len(jobs)
	}
	log.Printf("created %d production jobs", totalJobs)

	created, unbatchable, held, err := server.AutoCreateBatches(ctx)
	if err != nil {
		log.Fatalf("auto create batches: %v", err)
	}
	log.Printf("created %d batches, %d jobs unbatchable, %d partitions held below target",
		len(created), len(unbatchable), len(held))
	for _, u := range unbatchable {
		log.Printf("  unbatchable: %s - %s", u.JobNumber, u.Reason)
	}
	for _, h := range held {
		log.Printf("  held: %d jobs at %.1f%% utilisation", len(h.Jobs), h.BedUtilisationPercent)
	}
}
