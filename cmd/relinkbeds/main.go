// Command relinkbeds repairs finished beds whose jobs were detached from them.
//
// A one-off. MarkOrdersPrinted used to complete an order's jobs before marking
// the bed Done, and completing a job takes it off a bed that has not printed yet
// - so the bed was already empty when it was finished. The Completed list reads
// a bed's orders and colours from the jobs pointing at it, so those beds render
// a row of dashes: four planks, no idea whose.
//
// The record survived anyway, in the merged plate's filename. Approval names the
// plate after the orders on the bed and its colour:
//
//	114682-114716-114737-114748-RED.3mf
//
// So this reads each finished bed's plate name and puts its jobs back. Matching
// is deliberately narrow - the order number AND the plate's colour AND a job
// that is completed and currently on no bed - so a job that belongs to a
// different bed of the same order cannot be pulled onto this one.
//
//	go run ./cmd/relinkbeds -dry-run
//	go run ./cmd/relinkbeds
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
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would change without writing anything")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	rows, err := store.Pool.Query(ctx, `
		SELECT b.id, b.batch_number, f.filename
		  FROM batches b
		  JOIN file_assets f ON f.id = b.merged_file_id
		 WHERE b.status = 'completed'
		   AND NOT EXISTS (SELECT 1 FROM production_jobs j WHERE j.batch_id = b.id)
		 ORDER BY b.batch_number`)
	if err != nil {
		log.Fatalf("list finished beds: %v", err)
	}
	type bed struct {
		id       uuid.UUID
		number   string
		orders   []string
		colour   string
		relinked int
	}
	var beds []bed
	for rows.Next() {
		var b bed
		var filename string
		if err := rows.Scan(&b.id, &b.number, &filename); err != nil {
			log.Fatalf("scan: %v", err)
		}
		b.orders, b.colour = parsePlateName(filename)
		if len(b.orders) == 0 || b.colour == "" {
			log.Printf("%s: cannot read %q as a plate name, skipping", b.number, filename)
			continue
		}
		beds = append(beds, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("read finished beds: %v", err)
	}

	const match = `
		SELECT j.id FROM production_jobs j
		  JOIN orders o ON o.id = j.order_id
		 WHERE o.order_number LIKE '%' || $1
		   AND j.status = 'completed'
		   AND j.batch_id IS NULL
		   AND upper(coalesce(j.colour, '')) = $2
		 ORDER BY j.created_at, j.id
		 LIMIT 1`

	for i := range beds {
		b := &beds[i]
		for _, number := range b.orders {
			var jobID uuid.UUID
			err := store.Pool.QueryRow(ctx, match, number, b.colour).Scan(&jobID)
			if err != nil {
				// No completed, unattached job for that order in that colour.
				// Expected for a plank that was never detached, or one already
				// put back by an earlier run.
				continue
			}
			b.relinked++
			if *dryRun {
				continue
			}
			if _, err := store.Pool.Exec(ctx,
				`UPDATE production_jobs SET batch_id = $1, updated_at = now() WHERE id = $2`,
				b.id, jobID); err != nil {
				log.Fatalf("%s: relink %s: %v", b.number, number, err)
			}
		}
		fmt.Printf("%s  %s  %d of %d planks relinked\n", b.number, b.colour, b.relinked, len(b.orders))
	}
	if *dryRun {
		fmt.Println("\ndry run: nothing was written")
	}
}

// parsePlateName splits "114682-114716-114737-114748-RED.3mf" into its order
// numbers and colour. The colour is the last segment and may itself contain an
// underscore ("SKY_BLUE"), which is how a two-word colour survives a filename.
func parsePlateName(filename string) (orders []string, colour string) {
	stem := strings.TrimSuffix(filename, ".3mf")
	parts := strings.Split(stem, "-")
	if len(parts) < 2 {
		return nil, ""
	}
	colour = strings.ToUpper(strings.ReplaceAll(parts[len(parts)-1], "_", " "))
	return parts[:len(parts)-1], colour
}
