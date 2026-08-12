-- +goose Up
-- A discarded create_jobs_from_order job used to leave no trace: after five
-- attempts River gave up and the order simply sat there with zero production
-- jobs, status still 'queued', and nothing anywhere recording why. The only
-- recovery was a human noticing the absence.
--
-- orders.status cannot carry this. It is written once at import and never
-- updated (there is no UPDATE orders SET status query anywhere), and the
-- frontend derives the status it displays from financial_status, not from this
-- column - so a value written here would be invisible. Two explicit columns
-- instead: what went wrong, and when it was recorded. Both null on a healthy
-- order, so this is purely additive with no backfill.
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS job_creation_error     text,
    ADD COLUMN IF NOT EXISTS job_creation_failed_at timestamptz;

-- Partial: the overwhelming majority of orders are healthy, and the only query
-- that reads these wants the failures newest-first.
CREATE INDEX IF NOT EXISTS ix_orders_job_creation_failed
    ON orders (job_creation_failed_at DESC)
    WHERE job_creation_error IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_orders_job_creation_failed;
ALTER TABLE orders
    DROP COLUMN IF EXISTS job_creation_failed_at,
    DROP COLUMN IF EXISTS job_creation_error;
