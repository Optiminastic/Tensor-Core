// Command rerender re-renders every personalised model against the current
// OpenSCAD templates.
//
// The templates are embedded in the binary, so a change to them does not reach
// the models already in object storage - those were rendered by whatever
// template shipped at the time. After a template fix (a descender clipping
// through the plate, a name losing its last letter) every job has to be built
// again for the fix to reach the floor.
//
// Enqueues rather than rendering here: OpenSCAD is 20-45 seconds of CPU per
// plank, and the pipeline already has a queue that bounds how many run at once.
//
//	go run ./cmd/rerender -dry-run
//	go run ./cmd/rerender
//	go run ./cmd/rerender -job JOB-115059-2,JOB-115059-4
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
	dryRun := flag.Bool("dry-run", false, "report what would be queued without queueing it")
	includePrinted := flag.Bool("include-printed", false,
		"re-render completed jobs too - their model is the record a reprint would use")
	// Without this, the only choice is "every queued job", which is the wrong
	// tool for a change that affects one product: it rebuilds dozens of models
	// that were already correct, and a job sitting in a locked batch has its
	// file replaced underneath it.
	match := flag.String("match", "",
		"only jobs whose SKU or product name contains this, case-insensitive (e.g. \"photo frame\")")
	// Named jobs, whatever their status.
	//
	// -include-printed is the blunt version of this: it adds EVERY completed
	// job, which for a live family is hundreds of renders at 30-45s each to
	// correct the handful that are actually wrong. Naming the jobs is the
	// precise version, and naming one is itself the statement that its status
	// does not matter - the point is to correct the record a reprint and the
	// job page are read from, for a plank that has already printed.
	//
	// The bed's merged plate is deliberately NOT rebuilt by this: cmd/replate
	// skips completed beds, because their plate is the record of what was
	// actually printed and rewriting it would erase the evidence.
	jobNumbers := flag.String("job", "",
		"re-render exactly these job numbers, comma-separated, whatever their status")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Queued jobs by default: their plank has not printed, so a better model
	// reaches the floor.
	//
	// -include-printed adds the ones that have. Their plank is already on a
	// shelf and re-rendering will not change it - but the model is what a
	// REPRINT is built from, and what the job page shows an operator checking a
	// complaint. A model known to be wrong is worth correcting on both counts.
	// What actually printed is preserved either way: that lives in the batch's
	// merged plate file, which this never touches.
	where := "j.status = 'queued'"
	if *includePrinted {
		where = "j.status IN ('queued', 'completed')"
	}
	// Matched against the SKU *or* the product name, because neither alone
	// covers a family: nine plank lines and one photo frame carry no SKU at
	// all and are identified by name.
	args := []any{}
	// Named jobs win outright: -job says which rows, so status and -match have
	// nothing left to narrow.
	if named := splitJobNumbers(*jobNumbers); len(named) > 0 {
		where = "j.job_number = ANY($1::text[])"
		args = append(args, named)
	} else if *match != "" {
		where += ` AND (coalesce(j.sku, '') ILIKE '%' || $1 || '%'
		            OR coalesce(j.product_name, '') ILIKE '%' || $1 || '%')`
		args = append(args, *match)
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT j.id, j.job_number
		  FROM production_jobs j
		  JOIN orders o ON o.id = j.order_id
		 WHERE `+where+`
		 ORDER BY COALESCE(o.placed_at, j.created_at), j.job_number`, args...)
	if err != nil {
		log.Fatalf("list jobs: %v", err)
	}
	type job struct {
		id     uuid.UUID
		number string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.number); err != nil {
			log.Fatalf("scan: %v", err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("read jobs: %v", err)
	}

	fmt.Printf("%d queued jobs to re-render\n", len(jobs))
	if *dryRun {
		for _, j := range jobs[:min(5, len(jobs))] {
			fmt.Println("  ", j.number)
		}
		if len(jobs) > 5 {
			fmt.Printf("   … and %d more\n", len(jobs)-5)
		}
		fmt.Println("\ndry run: nothing was queued")
		return
	}

	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		log.Fatalf("build river client: %v", err)
	}
	enqueuer := production.NewModelGenEnqueuer(client)

	queued := 0
	for _, j := range jobs {
		if err := enqueuer.Enqueue(ctx, j.id); err != nil {
			log.Printf("%s: %v", j.number, err)
			continue
		}
		queued++
	}
	fmt.Printf("queued %d renders\n", queued)
}

// splitJobNumbers reads the -job flag into job numbers, ignoring blanks so a
// trailing comma or a stray space does not select nothing at all.
func splitJobNumbers(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if n := strings.TrimSpace(part); n != "" {
			out = append(out, n)
		}
	}
	return out
}
