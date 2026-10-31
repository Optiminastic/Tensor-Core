// Package retention removes finished work once it is older than the shop
// keeps, and leaves a summary of each day behind.
//
// THE SUMMARY IS WRITTEN FIRST. A day is rolled up - what it sold, what it
// printed - before a single row of it is deleted, so a crash between the two
// leaves the day summarised rather than silently gone. Re-running is safe: the
// summary upserts on (brand, window) and the deletes find nothing left.
//
// ORDER MATTERS AND IS NOT NEGOTIABLE. Jobs are deleted before orders, because
// production_jobs.order_id is ON DELETE SET NULL - delete the orders first and
// their jobs survive with no order, which means never rendered, never
// released, and sitting in the queue reading "Creating" for ever. Nine of them
// accumulated that way once already.
//
// WHAT IT NEVER TOUCHES: anything still in flight, however old. An order is
// removable only once DELIVERED, not dispatched - a parcel in transit can
// still come back as a damage claim, and damage_reports point at the order.
package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// ObjectStore is the slice of object storage a sweep needs: removing the
// models whose jobs have gone.
type ObjectStore interface {
	Delete(ctx context.Context, key string) error
}

// Options is one sweep's configuration.
type Options struct {
	// Days of finished history to keep.
	Days int
	// DryRun reports and deletes nothing.
	DryRun bool
	// MaxDays bounds one pass, so a first run against months of history does
	// not walk the whole backlog in one go.
	MaxDays int
	// Now is injectable for tests.
	Now func() time.Time
	// Zone is the shop's own clock: the day runs noon to noon IST, matching
	// the sales register rather than the calendar.
	Zone *time.Location
}

// Result is what one sweep did, for one day of one brand.
type Result struct {
	WindowEnd      time.Time
	BrandSlug      string
	Orders         int
	Jobs           int
	Batches        int
	FilesRemoved   int
	DeletedOrders  int64
	DeletedJobs    int64
	DeletedBatches int64
	DryRun         bool
}

const defaultMaxDays = 7

// Sweep summarises and removes one pass worth of expired days.
//
// Returns one Result per day touched, oldest first, so the caller can log
// exactly what went - which in a dry run is the entire point of running it.
func Sweep(
	ctx context.Context, store *db.Store, objects ObjectStore, brands []string, opts Options,
) ([]Result, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Zone == nil {
		opts.Zone = time.UTC
	}
	if opts.MaxDays <= 0 {
		opts.MaxDays = defaultMaxDays
	}
	if opts.Days <= 0 {
		return nil, fmt.Errorf("retention days must be positive, got %d", opts.Days)
	}

	cutoff := opts.Now().Add(-time.Duration(opts.Days) * 24 * time.Hour)
	var out []Result

	for _, brand := range brands {
		// ONE PASS WALKS FORWARD. after is the floor for "which day next",
		// and it has to exist because a DRY RUN deletes nothing: without it
		// the next question returns the same oldest order and the pass
		// reports a single day MaxDays times. The first dry run did exactly
		// that - 6 September, seven times - which is what a dry run is for.
		// A REAL TIMESTAMP, not the zero value: timestamptz maps a zero time
		// to NULL, and `created_at >= NULL` is never true - so the first
		// question of every pass would return no rows and the sweep would
		// report "nothing to do" against a full backlog.
		after := time.Unix(0, 0).UTC()
		for day := 0; day < opts.MaxDays; day++ {
			res, done, err := sweepOneDay(ctx, store, objects, brand, cutoff, after, opts)
			if err != nil {
				return out, err
			}
			if done {
				break
			}
			out = append(out, res)
			after = res.WindowEnd
		}
	}
	return out, nil
}

// sweepOneDay handles the oldest trading day still holding expired orders.
//
// done is true when there is nothing left for this brand, which is how the
// caller stops without having to count days itself.
func sweepOneDay(
	ctx context.Context, store *db.Store, objects ObjectStore,
	brand string, cutoff, after time.Time, opts Options,
) (Result, bool, error) {
	oldest, err := store.Q.OldestDeletableOrderWindow(ctx,
		gen.OldestDeletableOrderWindowParams{Cutoff: timestamptz(cutoff), After: timestamptz(after)})
	if err != nil || !oldest.Valid {
		// No rows, or nothing old enough. Either way this brand is finished.
		return Result{}, true, nil
	}

	from, to := shopDay(oldest.Time, opts.Zone)
	if !to.Before(cutoff) {
		// The oldest day is partly inside the retention window. Stop rather
		// than delete half a day somebody is still allowed to look at.
		return Result{}, true, nil
	}

	res := Result{WindowEnd: to, BrandSlug: brand, DryRun: opts.DryRun}

	orders, err := store.Q.SummariseOrdersInWindow(ctx, gen.SummariseOrdersInWindowParams{
		FromAt: timestamptz(from), ToAt: timestamptz(to), BrandSlug: &brand,
	})
	if err != nil {
		return res, false, fmt.Errorf("summarise orders: %w", err)
	}
	if orders.Orders == 0 {
		// The oldest expired day belongs to ANOTHER brand. Nothing here.
		return Result{}, true, nil
	}

	jobs, err := store.Q.SummariseJobsInWindow(ctx, gen.SummariseJobsInWindowParams{
		FromAt: timestamptz(from), ToAt: timestamptz(to), BrandSlug: &brand,
	})
	if err != nil {
		return res, false, fmt.Errorf("summarise jobs: %w", err)
	}

	res.Orders = int(orders.Orders)
	res.Jobs = int(jobs.Completed + jobs.Failed)
	res.Batches = int(jobs.Batches)

	files, err := store.Q.ListJobFilesInWindow(ctx, gen.ListJobFilesInWindowParams{
		FromAt: timestamptz(from), ToAt: timestamptz(to), BrandSlug: &brand,
	})
	if err != nil {
		return res, false, fmt.Errorf("list job files: %w", err)
	}
	res.FilesRemoved = len(files)

	// THE SUMMARY GOES IN FIRST, with no deleted_at: it describes a day that
	// still exists. A dry run stops here, having written down what it found
	// and removed nothing.
	summary := gen.UpsertRetentionSummaryParams{
		ID: uuid.New(), BrandSlug: brand, WindowEnd: timestamptz(to),
		Orders: orders.Orders, LineItems: orders.LineItems,
		Revenue: orders.Revenue, Currency: orders.Currency,
		JobsCompleted: jobs.Completed, JobsFailed: jobs.Failed,
		Units: jobs.Units, Batches: jobs.Batches,
	}
	if _, err := store.Q.UpsertRetentionSummary(ctx, summary); err != nil {
		return res, false, fmt.Errorf("write the summary: %w", err)
	}
	if opts.DryRun {
		return res, false, nil
	}

	deletedJobs, err := store.Q.DeleteJobsInWindow(ctx, gen.DeleteJobsInWindowParams{
		FromAt: timestamptz(from), ToAt: timestamptz(to), BrandSlug: &brand,
	})
	if err != nil {
		return res, false, fmt.Errorf("delete jobs: %w", err)
	}
	deletedOrders, err := store.Q.DeleteOrdersInWindow(ctx, gen.DeleteOrdersInWindowParams{
		FromAt: timestamptz(from), ToAt: timestamptz(to), BrandSlug: &brand,
	})
	if err != nil {
		return res, false, fmt.Errorf("delete orders: %w", err)
	}
	deletedBatches, err := store.Q.DeleteFinishedEmptyBatches(ctx, timestamptz(cutoff))
	if err != nil {
		return res, false, fmt.Errorf("delete empty batches: %w", err)
	}
	res.DeletedJobs = deletedJobs
	res.DeletedOrders = deletedOrders
	res.DeletedBatches = deletedBatches

	// The bucket last, and best effort. A model left behind costs storage; a
	// sweep that failed here and retried would re-delete rows already gone.
	removed := 0
	for _, f := range files {
		if objects != nil {
			if err := objects.Delete(ctx, f.StorageKey); err != nil {
				continue
			}
		}
		if err := store.Q.DeleteFileAsset(ctx, f.ID); err != nil {
			continue
		}
		removed++
	}
	res.FilesRemoved = removed

	summary.DeletedAt = timestamptz(opts.Now())
	summary.DeletedOrders = int32(deletedOrders)
	summary.DeletedJobs = int32(deletedJobs)
	if _, err := store.Q.UpsertRetentionSummary(ctx, summary); err != nil {
		return res, false, fmt.Errorf("record the deletion: %w", err)
	}
	return res, false, nil
}

// shopDay is the noon-to-noon window containing t, in the shop's own clock.
//
// The same day the sales register covers, deliberately: a summary and the
// spreadsheet for one trading day have to describe the same hours, or neither
// can be checked against the other.
func shopDay(t time.Time, loc *time.Location) (from, to time.Time) {
	local := t.In(loc)
	noon := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc)
	if local.Before(noon) {
		noon = noon.AddDate(0, 0, -1)
	}
	return noon, noon.AddDate(0, 0, 1)
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}
