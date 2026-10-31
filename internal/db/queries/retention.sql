-- name: OldestDeletableOrderWindow :one
-- The earliest trading day that still has orders old enough to remove.
--
-- DELIVERED ONLY, and old enough. A parcel in transit can still come back as
-- a damage claim, and damage_reports point at the order - so "dispatched" is
-- not finished, "delivered" is. An order Shopify has not told us about yet
-- has a null delivery_status and is never touched.
SELECT min(created_at)::timestamptz AS oldest
FROM orders
WHERE created_at < sqlc.arg('cutoff')
  AND lower(coalesce(delivery_status, '')) = 'delivered';

-- name: SummariseOrdersInWindow :one
-- What one trading day sold, before any of it is deleted.
SELECT
    count(*)::int                                   AS orders,
    coalesce(sum(o.total_price), 0)::numeric(14,2)  AS revenue,
    coalesce(max(o.currency), 'INR')::varchar(8)    AS currency,
    coalesce((
        SELECT count(*) FROM order_line_items li
        JOIN orders oo ON oo.id = li.order_id
        WHERE oo.created_at >= sqlc.arg('from_at') AND oo.created_at < sqlc.arg('to_at')
          AND oo.brand_slug = sqlc.arg('brand_slug')
          AND lower(coalesce(oo.delivery_status, '')) = 'delivered'
    ), 0)::int                                      AS line_items
FROM orders o
WHERE o.created_at >= sqlc.arg('from_at') AND o.created_at < sqlc.arg('to_at')
  AND o.brand_slug = sqlc.arg('brand_slug')
  AND lower(coalesce(o.delivery_status, '')) = 'delivered';

-- name: SummariseJobsInWindow :one
-- What one trading day printed. Counted from the jobs of the orders that are
-- about to go, not from every job in the window: a job whose order is still
-- in flight is not being deleted and must not be counted as if it were.
SELECT
    coalesce(sum(CASE WHEN j.status = 'completed' THEN 1 ELSE 0 END), 0)::int AS completed,
    coalesce(sum(CASE WHEN j.status = 'failed' THEN 1 ELSE 0 END), 0)::int    AS failed,
    coalesce(sum(j.quantity), 0)::int                                        AS units,
    count(DISTINCT j.batch_id)::int                                          AS batches
FROM production_jobs j
JOIN orders o ON o.id = j.order_id
WHERE o.created_at >= sqlc.arg('from_at') AND o.created_at < sqlc.arg('to_at')
  AND o.brand_slug = sqlc.arg('brand_slug')
  AND lower(coalesce(o.delivery_status, '')) = 'delivered';

-- name: ListJobFilesInWindow :many
-- The stored files belonging to the jobs about to be deleted, so the bucket
-- does not keep paying for models nothing references.
SELECT DISTINCT f.id, f.storage_key
FROM production_jobs j
JOIN orders o ON o.id = j.order_id
JOIN file_assets f ON f.id = j.print_file_id
WHERE o.created_at >= sqlc.arg('from_at') AND o.created_at < sqlc.arg('to_at')
  AND o.brand_slug = sqlc.arg('brand_slug')
  AND lower(coalesce(o.delivery_status, '')) = 'delivered';

-- name: DeleteJobsInWindow :execrows
-- THE JOBS FIRST, ALWAYS. production_jobs.order_id is ON DELETE SET NULL, so
-- deleting the orders first would leave jobs with no order - and a job with no
-- order is never rendered and never released, sitting in the queue reading
-- "Creating" for ever. Nine of them accumulated that way once already.
DELETE FROM production_jobs j
USING orders o
WHERE j.order_id = o.id
  AND o.created_at >= sqlc.arg('from_at') AND o.created_at < sqlc.arg('to_at')
  AND o.brand_slug = sqlc.arg('brand_slug')
  AND lower(coalesce(o.delivery_status, '')) = 'delivered';

-- name: DeleteOrdersInWindow :execrows
-- Line items, confirmations, personalisation links and dispatch rows cascade.
DELETE FROM orders
WHERE created_at >= sqlc.arg('from_at') AND created_at < sqlc.arg('to_at')
  AND brand_slug = sqlc.arg('brand_slug')
  AND lower(coalesce(delivery_status, '')) = 'delivered';

-- name: DeleteFinishedEmptyBatches :execrows
-- A completed bed whose jobs have all gone. Left behind it would read as a
-- plate waiting to print with nothing on it.
DELETE FROM batches b
WHERE b.status = 'completed'
  AND b.created_at < sqlc.arg('cutoff')
  AND NOT EXISTS (SELECT 1 FROM production_jobs j WHERE j.batch_id = b.id);

-- name: DeleteFileAsset :exec
-- Best effort: a file still referenced by something under RESTRICT stays, and
-- the caller carries on rather than failing a day's sweep over one row.
DELETE FROM file_assets WHERE id = sqlc.arg('id');

-- name: UpsertRetentionSummary :one
-- Written BEFORE the deletes and updated after, so a crash between the two
-- leaves the day summarised rather than silently gone.
INSERT INTO retention_day_summaries (
    id, brand_slug, window_end, orders, line_items, revenue, currency,
    jobs_completed, jobs_failed, units, batches, deleted_at,
    deleted_orders, deleted_jobs
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('window_end'),
    sqlc.arg('orders'), sqlc.arg('line_items'), sqlc.arg('revenue'),
    sqlc.arg('currency'), sqlc.arg('jobs_completed'), sqlc.arg('jobs_failed'),
    sqlc.arg('units'), sqlc.arg('batches'), sqlc.narg('deleted_at'),
    sqlc.arg('deleted_orders'), sqlc.arg('deleted_jobs')
)
ON CONFLICT (brand_slug, window_end) DO UPDATE SET
    orders = excluded.orders, line_items = excluded.line_items,
    revenue = excluded.revenue, currency = excluded.currency,
    jobs_completed = excluded.jobs_completed, jobs_failed = excluded.jobs_failed,
    units = excluded.units, batches = excluded.batches,
    deleted_at = coalesce(excluded.deleted_at, retention_day_summaries.deleted_at),
    deleted_orders = excluded.deleted_orders, deleted_jobs = excluded.deleted_jobs
RETURNING *;

-- name: ListRetentionSummaries :many
SELECT * FROM retention_day_summaries
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY window_end DESC
LIMIT sqlc.arg('lim');
