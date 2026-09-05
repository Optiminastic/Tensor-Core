// Command reformbeds returns locked beds to Draft so the planner can rebuild
// them.
//
// A locked bed is never re-planned - that is the whole point of locking one -
// so a bed committed under an older rule keeps that rule for ever. Twenty-five
// of forty-six locked beds held fewer than four planks, from when a partial bed
// locked itself after a few hours' wait; and because locked beds are frozen, the
// OLDEST orders could end up stranded on a bed of two while newer ones filled a
// bed of four beside it.
//
// Reopening them puts their jobs back in the pool. The planner then reforms the
// lot oldest-order-first: full beds of four from the oldest work, and whatever
// is left over as a Draft that the next matching order completes.
//
// A bed whose plate is actually at a printer is never touched. That is checked
// against BambuBuddy's live queue, not against Tensor's own columns, because a
// stale pipeline id proves nothing about what a machine is doing.
//
//	go run ./cmd/reformbeds -dry-run
//	go run ./cmd/reformbeds
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bed is one locked bed considered for reforming.
type bed struct {
	id        uuid.UUID
	number    string
	units     int32
	queueItem *int32
}

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would be reopened without touching it")
	partialOnly := flag.Bool("partial-only", false,
		"reopen only beds holding fewer than the per-bed cap, leaving full ones as they are")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	cap := cfg.BatchMaxUnitsPerBed
	if cap < 1 {
		cap = production.MaxColourBatchUnits
	}

	// What is genuinely at a printer, from BambuBuddy rather than from Tensor's
	// own columns: a pipeline id records that a run was STARTED, which is not
	// the same as a plate waiting on a machine.
	live := liveQueueItems(ctx, cfg)

	rows, err := store.Pool.Query(ctx, `
		SELECT id, batch_number, units_per_bed, queue_item_id
		  FROM batches
		 WHERE status = 'open'
		 ORDER BY batch_number`)
	if err != nil {
		log.Fatalf("list locked beds: %v", err)
	}
	var beds []bed
	for rows.Next() {
		var b bed
		var units *int32
		if err := rows.Scan(&b.id, &b.number, &units, &b.queueItem); err != nil {
			log.Fatalf("scan: %v", err)
		}
		if units != nil {
			b.units = *units
		}
		beds = append(beds, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("read locked beds: %v", err)
	}

	var reopen []bed
	var skipped []string
	for _, b := range beds {
		if b.queueItem != nil && live[int(*b.queueItem)] {
			skipped = append(skipped, fmt.Sprintf("%s (queued at a printer)", b.number))
			continue
		}
		if *partialOnly && b.units >= int32(cap) {
			continue
		}
		reopen = append(reopen, b)
	}

	fmt.Printf("locked beds: %d\nto reopen: %d\n", len(beds), len(reopen))
	partial := 0
	for _, b := range reopen {
		if b.units < int32(cap) {
			partial++
		}
	}
	fmt.Printf("of those, holding fewer than %d planks: %d\n", cap, partial)
	for _, s := range skipped {
		fmt.Println("  skipped:", s)
	}
	if len(reopen) == 0 {
		return
	}
	if *dryRun {
		fmt.Println("\ndry run: nothing was reopened")
		return
	}

	done := 0
	for _, b := range reopen {
		if _, err := store.Q.ReopenBatchForReplanning(ctx, b.id); err != nil {
			log.Printf("%s: %v", b.number, err)
			continue
		}
		done++
	}
	fmt.Printf("\nreopened %d beds - run a replan to reform them\n", done)
}

// liveQueueItems is the set of BambuBuddy queue items still waiting or printing.
func liveQueueItems(ctx context.Context, cfg config.Settings) map[int]bool {
	client := bambubuddy.New(cfg.BambuBuddyURL, cfg.BambuBuddyAPIKey)
	if !client.Configured() {
		return nil
	}
	items, err := client.ListQueue(ctx)
	if err != nil {
		// Refusing to reform anything is the safe failure: a bed left locked can
		// be reopened later, a plate pulled from under a running printer cannot
		// be put back.
		log.Fatalf("could not read BambuBuddy's queue, so nothing was touched: %v", err)
	}
	live := map[int]bool{}
	for _, i := range items {
		if i.Waiting() {
			live[i.ID] = true
		}
	}
	return live
}
