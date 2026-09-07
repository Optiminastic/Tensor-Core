// Command backfillpriority stamps the urgent rank on jobs whose order paid for
// priority dispatch.
//
// jobPriorityRank does this at job creation, but only from the moment it
// existed. Every job already in the queue carries the schema default - normal -
// however the customer paid, so without this the Priority tab opens empty on a
// floor that has priority work sitting in it.
//
// Reads the same rule the live path does (httpapi.OrderIsPriority) rather than
// its own SQL LIKE, so the two cannot drift into disagreeing about which orders
// count.
//
//	go run ./cmd/backfillpriority -dry-run
//	go run ./cmd/backfillpriority
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would be re-ranked without writing")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// nil brand: every store's orders, since priority is a property of the
	// shipping option rather than of any one brand.
	orders, err := store.Q.ListOrders(ctx, nil)
	if err != nil {
		log.Fatalf("list orders: %v", err)
	}

	var priorityOrders []string
	ids := make([]any, 0)
	for _, o := range orders {
		if !httpapi.OrderIsPriority(o) {
			continue
		}
		priorityOrders = append(priorityOrders, o.OrderNumber)
		ids = append(ids, o.ID)
	}

	fmt.Printf("orders: %d\norders that paid for priority: %d\n", len(orders), len(priorityOrders))
	if len(ids) == 0 {
		return
	}

	var affected int64
	err = store.Pool.QueryRow(ctx, `
		SELECT count(*) FROM production_jobs
		 WHERE order_id = ANY($1::uuid[]) AND priority > $2`, ids, httpapi.PriorityRank).
		Scan(&affected)
	if err != nil {
		log.Fatalf("count jobs: %v", err)
	}
	fmt.Printf("jobs to re-rank: %d\n", affected)
	for _, n := range priorityOrders {
		fmt.Println("  ", n)
	}
	if affected == 0 {
		return
	}
	if *dryRun {
		fmt.Println("\ndry run: nothing was written")
		return
	}

	// Only ever makes a job MORE urgent, never less: a line escalated by hand
	// below PriorityRank keeps the rank somebody gave it deliberately.
	tag, err := store.Pool.Exec(ctx, `
		UPDATE production_jobs SET priority = $2, updated_at = now()
		 WHERE order_id = ANY($1::uuid[]) AND priority > $2`, ids, httpapi.PriorityRank)
	if err != nil {
		log.Fatalf("re-rank jobs: %v", err)
	}
	fmt.Printf("\nre-ranked %d job(s) - run a replan to reform the beds around them\n", tag.RowsAffected())
}
