// Command fleetsync reconciles Tensor's machine list with the printers
// BambuBuddy actually has connected.
//
// Every printer BambuBuddy reports is created or refreshed - name, status,
// loaded filament, live layer progress, and the slicing profile matching its
// installed nozzles. Anything else in the fleet is removed, which is what
// clears placeholder machines out of Machine Management.
//
// Safe to re-run and safe to schedule: it is keyed on each printer's serial
// number, so a rename updates a machine rather than replacing it, and a unit
// mid-print is never removed.
//
//	go run ./cmd/fleetsync
package main

import (
	"context"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	if cfg.BambuBuddyURL == "" || cfg.BambuBuddyAPIKey == "" {
		log.Fatal("BAMBUBUDDY_URL and BAMBUBUDDY_API_KEY must both be set")
	}

	logger := obs.New("info")
	ctx := obs.WithLogger(context.Background(), logger)

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer store.Close()

	// Never serves HTTP - a nil verifier is safe, matching cmd/productionworker.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)

	result, err := server.SyncFleetFromBambuBuddy(ctx)
	if err != nil {
		log.Fatalf("sync fleet: %v", err)
	}
	log.Printf("synced %d printer(s), removed %d: %v", result.Synced, result.Removed, result.Names)
}
