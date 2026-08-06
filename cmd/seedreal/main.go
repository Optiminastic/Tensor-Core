// Command seedreal is a one-off dev-data tool: it uploads real STL/3MF files
// from a local folder as approved designs (sliced via the running FAKE_SLICE
// worker, not fabricated here), clears the old placeholder dummy orders, then
// generates realistic orders referencing the real designs' SKUs and runs the
// real pipeline (job creation, batching) so the whole order -> job -> batch
// -> machine flow can be exercised end to end with real geometry.
//
// Not part of cmd/seed's idempotent bootstrap - this is explicit, ad-hoc
// test-data generation tied to a specific local folder and a specific brand,
// not something every developer/environment needs. Safe to re-run: old seed
// orders are cleared first, and re-running just creates a fresh 20 orders
// against whatever designs exist.
package main

import (
	"context"
	"log"
	"time"

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
	stlDir    = `C:\Users\optiminastic\Desktop\stl_files`
	createdBy = "seed-script"
)

func main() {
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
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey,
		Bucket: cfg.MinIOBucket, KeyPrefix: cfg.MinIOKeyPrefix, Secure: cfg.MinIOSecure,
		AssumeBucketExists: cfg.MinIOAssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}

	// This process never serves HTTP requests - a nil verifier/enqueuer is
	// safe, matching cmd/productionworker's exact construction pattern.
	guards := auth.NewGuards(nil, "")
	server := httpapi.NewServer(cfg, store, guards, logger)
	server.EnablePipeline(objects, nil)

	machineID, err := findH2CProfile(ctx, store)
	if err != nil {
		log.Fatalf("find H2C machine profile (run `go run ./cmd/seed` first): %v", err)
	}

	if err := resetSeedData(ctx, store); err != nil {
		log.Fatalf("reset previous seed data: %v", err)
	}
	log.Println("cleared previous seed run's jobs, batches, orders, and designs")

	files, err := stlFiles(stlDir)
	if err != nil {
		log.Fatalf("list model files in %s: %v", stlDir, err)
	}
	log.Printf("found %d model files in %s", len(files), stlDir)

	designs, err := createDesigns(ctx, store, objects, files, machineID)
	if err != nil {
		log.Fatalf("create designs: %v", err)
	}
	log.Printf("created %d designs, queued for slicing (FAKE_SLICE worker must be running)", len(designs))

	priced, err := waitForSlicing(ctx, store, designs, 3*time.Minute)
	if err != nil {
		log.Fatalf("wait for slicing: %v", err)
	}
	log.Printf("%d/%d designs sliced successfully", len(priced), len(designs))

	if err := approveDesigns(ctx, store, priced); err != nil {
		log.Fatalf("approve designs: %v", err)
	}
	log.Printf("approved %d designs with SKUs assigned", len(priced))

	orderIDs, err := seedRealOrders(ctx, store, priced)
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
		log.Printf("  unbatchable: job %s (%s): %s", u.JobID, u.JobNumber, u.Reason)
	}
	for _, h := range held {
		log.Printf("  held: %d jobs at %.2f%% utilisation", len(h.Jobs), h.BedUtilisationPercent)
	}
}
