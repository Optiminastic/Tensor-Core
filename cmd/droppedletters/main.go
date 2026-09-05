// Command droppedletters finds planks that printed with letters missing.
//
// Until 4 September the no-heart template dropped a padding slot outright
// rather than filling it, and a padding slot is what holds the extra letters of
// the LONGER name. So a plank for KAUSTUBH and LIPIKA printed "AUSTUB": eight
// letters squeezed into six slots, losing one from each end. JESUS and CHRIST
// printed CHRIS the same way.
//
// It only ever affected orders with NO hearts. The one- and two-heart templates
// fill a padding slot with a heart, so every letter kept its place.
//
// The template is fixed and every model has been rebuilt, but a plank that has
// already printed is on a shelf and nobody downstream knows it is wrong. This
// names them, so somebody can decide what to reprint.
//
//	go run ./cmd/droppedletters
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// templateFixedAt is when the corrected templates were embedded. A model built
// before it came from the version that dropped padding slots.
var templateFixedAt = time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Every plank whose model predates the fix, with its order's line items.
	// The heart count is not stored on the job, so it is re-read from the line
	// exactly as the renderer read it.
	rows, err := store.Pool.Query(ctx, `
		SELECT j.job_number, j.status, o.order_number, o.line_items, j.sku, j.product_name
		  FROM production_jobs j
		  JOIN file_assets f ON f.id = j.print_file_id
		  JOIN orders o ON o.id = j.order_id
		 WHERE f.created_at < $1
		 ORDER BY j.job_number`, templateFixedAt)
	if err != nil {
		log.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()

	type hit struct {
		job, status, order, left, right string
	}
	var affected []hit
	checked := 0

	for rows.Next() {
		var job, status, order string
		var raw []byte
		var sku, product *string
		if err := rows.Scan(&job, &status, &order, &raw, &sku, &product); err != nil {
			log.Fatalf("scan: %v", err)
		}
		var items []production.LineItem
		if err := json.Unmarshal(raw, &items); err != nil {
			continue
		}
		params, ok := plankParams(items)
		if !ok {
			// A line that is not a plank, or an order missing its options -
			// neither is evidence of a dropped letter.
			continue
		}
		checked++

		// The bug needed both: no heart to fill the padding slot, and names of
		// different lengths to need one.
		if params.Hearts == 0 && len([]rune(params.NameLeft)) != len([]rune(params.NameRight)) {
			affected = append(affected, hit{job, status, order, params.NameLeft, params.NameRight})
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("read jobs: %v", err)
	}

	fmt.Printf("planks built before the template fix: %d\n", checked)
	fmt.Printf("of those, printed with letters missing: %d\n\n", len(affected))
	if len(affected) == 0 {
		return
	}
	fmt.Printf("%-14s %-11s %-14s %s\n", "JOB", "STATUS", "ORDER", "NAMES")
	for _, a := range affected {
		fmt.Printf("%-14s %-11s %-14s %s / %s\n", a.job, a.status, a.order, a.left, a.right)
	}
	fmt.Println("\nA job still queued has been re-rendered and will print correctly.")
	fmt.Println("A completed one has already printed wrong and needs a decision.")
}

// plankParams resolves a plank's render inputs from whichever line of an order
// carries personalisation options.
//
// The job's own SKU is not used to pick the line: these orders predate parts of
// the import, and several carry no SKU at all. The first line with options is
// the plank - a DNP order has exactly one.
func plankParams(items []production.LineItem) (personalise.Params, bool) {
	for _, li := range items {
		if len(li.Properties) == 0 {
			continue
		}
		params, err := personalise.ParamsFromProperties(li.Properties)
		if err != nil {
			continue
		}
		return params, true
	}
	return personalise.Params{}, false
}
