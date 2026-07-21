// Command seed applies the migrations and seeds the RBAC catalog and the two
// brands. It is idempotent -- safe to run repeatedly.
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
	_ = godotenv.Load()
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	ctx := context.Background()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	store, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	authRes, err := auth.SyncAll(ctx, store)
	if err != nil {
		log.Fatalf("seed auth: %v", err)
	}
	brands, err := brandpolicy.SyncBrands(ctx, store)
	if err != nil {
		log.Fatalf("seed brands: %v", err)
	}

	log.Printf("seeded: %d permissions, %d roles, %d grants, %d brands",
		authRes.Permissions, authRes.Roles, authRes.Grants, brands)
}
