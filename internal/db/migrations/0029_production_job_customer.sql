-- +goose Up
-- Denormalised customer identity from the parent order, mirroring how
-- shopify_order_id already works - avoids a per-job join in planJobsFor and
-- lets batch listings (GET /production-jobs?batch_id=) show which customer
-- each job belongs to without a second query. Both nullable: guest
-- checkouts, and jobs with no linked order at all, have neither.
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS shopify_customer_id bigint;
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS customer_name varchar(255);
CREATE INDEX IF NOT EXISTS ix_production_jobs_shopify_customer_id ON production_jobs (shopify_customer_id);

-- +goose Down
DROP INDEX IF EXISTS ix_production_jobs_shopify_customer_id;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS customer_name;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS shopify_customer_id;
