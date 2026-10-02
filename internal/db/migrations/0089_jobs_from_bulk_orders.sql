-- +goose Up
-- Which bulk order a job came from.
--
-- A job made from a quotation has no Shopify order behind it: order_id and
-- shopify_order_id are both null, which is already allowed, and without this
-- column there would be nothing at all tying the job to the customer who asked
-- for it. "Where did these hundred planks come from" has to have an answer on
-- the job itself, not only in somebody's memory of which spreadsheet was
-- uploaded.
--
-- ON DELETE SET NULL rather than CASCADE: deleting a quotation must not delete
-- work that may already be printing. The job survives, pointing at nothing,
-- which is the honest record of what happened.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS bulk_order_id uuid REFERENCES bulk_orders (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS ix_production_jobs_bulk_order
    ON production_jobs (bulk_order_id) WHERE bulk_order_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_production_jobs_bulk_order;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS bulk_order_id;
