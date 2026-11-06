package retention

// The rule since 2026-11-06: age is the only test. Everything older than the
// retention period goes - undelivered, unshipped, cancelled, orphaned - and
// nothing younger than it is touched.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db"
)

func retentionTestStore(t *testing.T) *db.Store {
	t.Helper()
	dsn := os.Getenv("TENSOR_TEST_DB")
	if dsn == "" {
		t.Skip("set TENSOR_TEST_DB to run integration tests")
	}
	ctx := context.Background()
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := db.Open(ctx, dsn, db.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.Pool.Exec(ctx,
		`TRUNCATE production_jobs, orders, batches, retention_day_summaries CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return store
}

func TestIntegrationEverythingOlderThanTheRetentionPeriodGoes(t *testing.T) {
	store := retentionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -45)
	recent := now.AddDate(0, 0, -5)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	order := func(id, num string, at time.Time, brand any, delivery any) {
		exec(`INSERT INTO orders (id, shopify_order_id, order_number, financial_status, total_price,
		        currency, line_items, status, brand_slug, delivery_status, created_at)
		      VALUES ($1, $2, $3, 'paid', 0, 'INR', '[]', 'imported', $4, $5, $6)`,
			id, time.Now().UnixNano(), num, brand, delivery, at)
	}
	job := func(id, num string, orderID any, batchID any, at time.Time) {
		exec(`INSERT INTO production_jobs (id, job_number, description, order_id, batch_id, created_at)
		      VALUES ($1, $2, 'test', $3, $4, $5)`, id, num, orderID, batchID, at)
	}
	exec(`INSERT INTO batches (id, batch_number, status, created_at)
	      VALUES ('00000000-0000-0000-0000-0000000000b1', 'B-OLD', 'pending_approval', $1),
	             ('00000000-0000-0000-0000-0000000000b2', 'B-NEW', 'pending_approval', $2)`, old, recent)

	// Old, in every state the previous rule kept for ever.
	order("00000000-0000-0000-0000-000000000001", "O-UNDELIVERED", old, "shop", nil)
	order("00000000-0000-0000-0000-000000000002", "O-DELIVERED", old, "shop", "delivered")
	order("00000000-0000-0000-0000-000000000003", "O-RTO", old.Add(time.Hour), "shop", "rto")
	order("00000000-0000-0000-0000-000000000004", "O-NOBRAND", old, nil, nil)
	job("00000000-0000-0000-0000-00000000000a", "J-OLD", "00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-0000000000b1", old)
	job("00000000-0000-0000-0000-00000000000b", "J-ORPHAN", nil, nil, old)
	job("00000000-0000-0000-0000-00000000000c", "J-NOBRAND", "00000000-0000-0000-0000-000000000004", nil, old)
	// Recent: must survive untouched.
	order("00000000-0000-0000-0000-000000000005", "O-RECENT", recent, "shop", nil)
	job("00000000-0000-0000-0000-00000000000d", "J-RECENT", "00000000-0000-0000-0000-000000000005",
		"00000000-0000-0000-0000-0000000000b2", recent)

	if _, err := Sweep(ctx, store, nil, []string{"shop"}, Options{
		Days: 30, Now: func() time.Time { return now }, Zone: time.UTC,
	}); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	left := func(table string) []string {
		t.Helper()
		col := map[string]string{"orders": "order_number", "production_jobs": "job_number", "batches": "batch_number"}[table]
		rows, err := store.Pool.Query(ctx, `SELECT `+col+` FROM `+table+` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	for table, want := range map[string]string{
		"orders": "O-RECENT", "production_jobs": "J-RECENT", "batches": "B-NEW",
	} {
		got := left(table)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s left = %v, want only [%s]", table, got, want)
		}
	}
}

func TestIntegrationADryRunDeletesNothing(t *testing.T) {
	store := retentionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO orders (id, shopify_order_id, order_number, financial_status, total_price,
		   currency, line_items, status, brand_slug, created_at)
		 VALUES (gen_random_uuid(), 1, 'O-OLD', 'paid', 0, 'INR', '[]', 'imported', 'shop', $1)`,
		now.AddDate(0, 0, -45)); err != nil {
		t.Fatal(err)
	}
	res, err := Sweep(ctx, store, nil, []string{"shop"}, Options{
		Days: 30, DryRun: true, Now: func() time.Time { return now }, Zone: time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if n != 1 || len(res) != 1 || res[0].Orders != 1 {
		t.Fatalf("dry run: %d orders left, results %+v; want the order kept and reported", n, res)
	}
}
