// Command seed applies the migrations and seeds the RBAC catalog, the default
// cost assumptions, 20 dummy orders (for the frontend's live/dummy orders
// toggle), and a default 3-machine fleet (for the batch scheduler). Brands are
// user-created, so none are seeded. It is idempotent -- safe to run repeatedly.
package main

import (
	"context"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/brandpolicy"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	ctx := context.Background()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := db.MigrateRiver(ctx, cfg.DatabaseURL); err != nil {
		log.Fatalf("river migrate: %v", err)
	}

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	authRes, err := auth.SyncAll(ctx, store)
	if err != nil {
		log.Fatalf("seed auth: %v", err)
	}
	costSets, err := brandpolicy.SyncDefaultCostAssumptions(ctx, store)
	if err != nil {
		log.Fatalf("seed cost assumptions: %v", err)
	}
	dummyOrders, err := seedDummyOrders(ctx, store)
	if err != nil {
		log.Fatalf("seed dummy orders: %v", err)
	}
	fleetMachines, err := seedFleetMachines(ctx, store)
	if err != nil {
		log.Fatalf("seed fleet machines: %v", err)
	}

	log.Printf("seeded: %d permissions, %d roles, %d grants, %d cost sets, %d dummy orders, %d fleet machines",
		authRes.Permissions, authRes.Roles, authRes.Grants, costSets, dummyOrders, fleetMachines)
}
