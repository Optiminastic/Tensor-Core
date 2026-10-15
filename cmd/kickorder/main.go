// Command kickorder asks the production pipeline to take an order again.
//
// It enqueues exactly what the Shopify import enqueues - one
// create_jobs_from_order trigger per order - and then stands back. The
// production worker does the rest: builds the jobs, renders the models Tensor
// renders, opens the customer's window, sends the link, and closes the window
// when it runs out.
//
// So it is a TRIGGER, not a second implementation of any of that. The same
// shape as cmd/kickdispatch and cmd/rerender beside it, and for the same
// reason: the pipeline is correct and occasionally needs poking at a specific
// order without waiting for a whole-brand sync to come round.
//
// Refuses an order that already has jobs unless -again is given, because the
// ordinary mistake is to re-run it on an order halfway through production.
//
// -link asks instead for the customer's window to be considered, which is
// what the render worker asks for the moment a model lands. The order's jobs
// already exist in that case; what has not happened is anybody asking the
// customer. The worker still applies every rule - the allow-list, the
// watermark, the phone, the test recipient - so this opens no window the
// pipeline would not have opened itself.
//
//	go run ./cmd/kickorder -order T3DPS-114666
//	go run ./cmd/kickorder -order T3DPS-114666,T3DPS-115735 -dry-run
//	go run ./cmd/kickorder -order T3DPS-115735 -link
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

func main() {
	orders := flag.String("order", "", "order numbers, comma-separated (e.g. T3DPS-114666)")
	dryRun := flag.Bool("dry-run", false, "report what would be queued without queueing it")
	again := flag.Bool("again", false, "allow an order that already has production jobs")
	link := flag.Bool("link", false,
		"ask for the customer's personalisation window instead of creating jobs")
	flag.Parse()

	names := split(*orders)
	if len(names) == 0 {
		log.Fatal("name at least one order with -order")
	}

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	rows, err := store.Pool.Query(ctx, `
		SELECT o.id, o.order_number,
		       (o.customer_phone IS NOT NULL AND o.customer_phone <> '') AS has_phone,
		       count(j.id) AS jobs
		  FROM orders o
		  LEFT JOIN production_jobs j ON j.order_id = o.id
		 WHERE o.order_number = ANY($1::text[])
		 GROUP BY o.id, o.order_number`, names)
	if err != nil {
		log.Fatalf("look up orders: %v", err)
	}
	type order struct {
		id       uuid.UUID
		number   string
		hasPhone bool
		jobs     int64
	}
	var found []order
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.id, &o.number, &o.hasPhone, &o.jobs); err != nil {
			log.Fatalf("scan: %v", err)
		}
		found = append(found, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("read orders: %v", err)
	}

	seen := map[string]bool{}
	for _, o := range found {
		seen[o.number] = true
	}
	for _, name := range names {
		if !seen[name] {
			log.Printf("%s: no such order", name)
		}
	}

	var queue []order
	for _, o := range found {
		// Said out loud rather than silently skipped. Without a number the
		// window cannot open, the link is never sent, and the jobs are
		// released unasked - which looks from the outside like the feature
		// being off.
		if !o.hasPhone {
			log.Printf("%s: no phone on the order, so no link will be sent", o.number)
		}
		// -link works ON an order that already has jobs, so the guard does
		// not apply to it.
		if o.jobs > 0 && !*again && !*link {
			log.Printf("%s: already has %d jobs, skipping (pass -again to force)", o.number, o.jobs)
			continue
		}
		queue = append(queue, o)
	}

	if *dryRun {
		for _, o := range queue {
			fmt.Printf("would queue %s\n", o.number)
		}
		fmt.Printf("\ndry run: nothing was queued (%d orders)\n", len(queue))
		return
	}

	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}
	if *link {
		sender := production.NewPersonalisationLinkEnqueuer(client)
		sent := 0
		for _, o := range queue {
			err := sender.Enqueue(ctx, production.SendPersonalisationLinkArgs{OrderID: o.id})
			if err != nil {
				log.Printf("%s: %v", o.number, err)
				continue
			}
			fmt.Printf("asked for %s window\n", o.number)
			sent++
		}
		fmt.Printf("asked for %d of %d windows\n", sent, len(names))
		return
	}

	enqueuer := production.NewJobCreationEnqueuer(client)

	queued := 0
	for _, o := range queue {
		// In its own transaction, the same way the import does it, so a
		// failure part way leaves the orders before it queued rather than
		// rolling the lot back.
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			log.Printf("%s: begin: %v", o.number, err)
			continue
		}
		if err := enqueuer.EnqueueTx(ctx, tx, production.CreateJobsArgs{OrderID: o.id}); err != nil {
			_ = tx.Rollback(ctx)
			log.Printf("%s: %v", o.number, err)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			log.Printf("%s: commit: %v", o.number, err)
			continue
		}
		fmt.Printf("queued %s\n", o.number)
		queued++
	}
	fmt.Printf("queued %d of %d orders\n", queued, len(names))
}

// split reads a comma-separated flag, ignoring blanks so a trailing comma or
// a stray space does not select nothing at all.
func split(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if n := strings.TrimSpace(part); n != "" {
			out = append(out, n)
		}
	}
	return out
}
