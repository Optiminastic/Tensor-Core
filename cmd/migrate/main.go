// Command migrate applies the goose migrations against DATABASE_URL.
package main

import (
	"context"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := db.MigrateRiver(context.Background(), cfg.DatabaseURL); err != nil {
		log.Fatalf("river migrate: %v", err)
	}
	log.Println("migrations applied")
}
