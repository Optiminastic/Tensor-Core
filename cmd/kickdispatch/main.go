// Enqueues one dispatch pass, by hand.
//
// Local only, and only because River leadership on this database is held by a
// client that no longer exists, so the periodic pass never fires here. Nothing
// about leadership stops a pass being WORKED - the dispatch worker in cmd/api
// consumes whatever lands on its queue - so inserting the job is enough.
//
// It uses production.DispatchEnqueuer rather than writing a river_job row, so
// the queue name, the args and the debounce are the ones the product uses.
package main

import (
	"context"
	"log"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer store.Close()

	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("river client: %v", err)
	}
	// Zero debounce: this is a deliberate single trigger, not one of six racing
	// plan results, so it must not be collapsed into a pass already queued.
	if err := production.NewDispatchEnqueuer(client, 0).Enqueue(ctx); err != nil {
		log.Fatalf("enqueue dispatch: %v", err)
	}
	log.Printf("dispatch pass enqueued at %s", time.Now().Format(time.TimeOnly))
}
