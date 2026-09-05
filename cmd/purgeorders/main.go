// Command purgeorders removes orders placed before the production cutoff,
// along with the production jobs and batches derived from them.
//
// Tensor imported the store's whole history before the pipeline was floored at
// 26 August 2026, so the database carries roughly two thousand orders that
// predate the current way of working. They create jobs nobody means to print
// and make every queue unreadable.
//
// It asks SHOPIFY which orders belong, rather than trusting a column: it lists
// the orders on or after the cutoff for every connected store and keeps exactly
// those. That matters because placed_at is null on everything imported before
// migration 0061, so the database alone cannot answer the question.
//
// Dry run by default. Pass -apply to actually delete.
//
//	go run ./cmd/purgeorders            # report only
//	go run ./cmd/purgeorders -apply     # delete
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// cutoff is the first day of the current production run - the same date the
// order sync is floored at, so the two can never disagree about what "current"
// means.
var cutoff = time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)

func main() {
	apply := flag.Bool("apply", false, "actually delete; without it, only report")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	keep, err := ordersToKeep(ctx, pool)
	if err != nil {
		log.Fatalf("ask shopify which orders belong: %v", err)
	}
	// A store that answers with nothing is far more likely to be a broken
	// token than a store with no recent orders, and acting on it would delete
	// everything. Refuse rather than guess.
	if len(keep) == 0 {
		log.Fatal("Shopify returned no orders since the cutoff; refusing to purge on an empty answer")
	}
	fmt.Printf("Shopify has %d orders on or after %s\n", len(keep), cutoff.Format("2006-01-02"))

	if err := purge(ctx, pool, keep, *apply); err != nil {
		log.Fatalf("purge: %v", err)
	}
}

// ordersToKeep is every Shopify order id at or after the cutoff, across every
// connected store.
func ordersToKeep(ctx context.Context, pool *pgxpool.Pool) ([]int64, error) {
	rows, err := pool.Query(ctx,
		`SELECT brand_slug, external_account_id, access_token FROM brand_connections
		 WHERE provider = 'shopify' AND status = 'connected'
		   AND access_token IS NOT NULL AND external_account_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type store struct{ slug, shop, token string }
	var stores []store
	for rows.Next() {
		var s store
		if err := rows.Scan(&s.slug, &s.shop, &s.token); err != nil {
			return nil, err
		}
		stores = append(stores, s)
	}

	client := shopify.New("2025-01", 60*time.Second)
	seen := make(map[int64]bool)
	var keep []int64
	for _, s := range stores {
		orders, err := client.ListRecentOrders(ctx, s.shop, s.token, 0, cutoff)
		if err != nil {
			// Fatal, not skipped: a store we could not reach is a store whose
			// orders we would delete for lack of an answer.
			return nil, fmt.Errorf("%s (%s): %w", s.slug, s.shop, err)
		}
		fmt.Printf("  %-32s %-40s %d orders\n", s.slug, s.shop, len(orders))
		for _, o := range orders {
			if o.ID != 0 && !seen[o.ID] {
				seen[o.ID] = true
				keep = append(keep, o.ID)
			}
		}
	}
	return keep, nil
}

// purge deletes in dependency order inside one transaction.
//
// Jobs before orders: production_jobs.order_id is ON DELETE SET NULL, so
// deleting an order would leave its jobs behind with a null order_id - still in
// the queue, now with nothing to trace them back to. Batches last, once the
// jobs that filled them are gone.
//
// Seeded orders are left alone. They are not what this is for, the Orders page
// already filters them out, and a cleanup should remove only what it was asked
// to remove.
func purge(ctx context.Context, pool *pgxpool.Pool, keep []int64, apply bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const doomed = `
		SELECT id FROM orders
		WHERE source <> 'seed' AND NOT (shopify_order_id = ANY($1::bigint[]))`

	var orders, jobs, batches int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM ( `+doomed+` ) d`, keep).Scan(&orders); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM production_jobs WHERE order_id IN ( `+doomed+` )`, keep).Scan(&jobs); err != nil {
		return err
	}

	// Which batches the doomed jobs were on, captured BEFORE they are deleted -
	// afterwards there is nothing left to trace the link.
	touched, err := batchIDsOfDoomedJobs(ctx, tx, doomed, keep)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM production_jobs WHERE order_id IN ( `+doomed+` )`, keep); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM orders WHERE id IN ( `+doomed+` )`, keep); err != nil {
		return err
	}
	// Only batches this purge actually emptied. A batch that was already empty
	// is somebody's draft and none of this command's business, which is why the
	// set is restricted to batches the doomed jobs were actually on.
	if len(touched) > 0 {
		tag, err := tx.Exec(ctx, `
			DELETE FROM batches b
			WHERE b.id = ANY($1::uuid[])
			  AND NOT EXISTS (SELECT 1 FROM production_jobs j WHERE j.batch_id = b.id)`, touched)
		if err != nil {
			return err
		}
		batches = tag.RowsAffected()
	}

	fmt.Printf("\n%s\n", map[bool]string{true: "DELETING:", false: "WOULD DELETE (dry run):"}[apply])
	fmt.Printf("  orders           %d\n", orders)
	fmt.Printf("  production jobs  %d\n", jobs)
	fmt.Printf("  empty batches    %d\n", batches)

	var remaining int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE source <> 'seed'`).Scan(&remaining); err != nil {
		return err
	}
	fmt.Printf("  orders remaining %d\n", remaining)

	if !apply {
		fmt.Println("\nDry run - nothing was changed. Re-run with -apply to delete.")
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Println("\nDone.")
	return nil
}

// batchIDsOfDoomedJobs is the distinct batches the about-to-be-deleted jobs sit
// on. Read before the delete, because afterwards the association is gone.
func batchIDsOfDoomedJobs(ctx context.Context, tx pgx.Tx, doomed string, keep []int64) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT batch_id FROM production_jobs
		 WHERE batch_id IS NOT NULL AND order_id IN ( `+doomed+` )`, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
