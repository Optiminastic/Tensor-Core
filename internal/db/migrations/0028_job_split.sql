-- +goose Up
-- Self-reference for a job whose quantity was split across multiple batches
-- because it didn't fit on one bed as a whole (e.g. 150 keychains, 18 fit per
-- bed -> the original row keeps shrinking by whatever gets peeled off into a
-- new split row per batch, down to whatever's left for the next planning
-- run). Mirrors reprint_of_job_id's self-reference pattern exactly. Null for
-- a job that was never split.
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS split_of_job_id uuid REFERENCES production_jobs (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS ix_production_jobs_split_of ON production_jobs (split_of_job_id);

-- +goose Down
DROP INDEX IF EXISTS ix_production_jobs_split_of;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS split_of_job_id;
