-- +goose Up
-- One slice job per (design, attempt). A resubmit reads the next attempt number
-- inside its transaction; this constraint makes two concurrent resubmits of the
-- same design mutually exclusive - the loser's insert conflicts and the whole
-- re-slice rolls back instead of creating a duplicate attempt. Idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS uq_slice_jobs_design_attempt ON slice_jobs (design_id, attempt);

-- +goose Down
DROP INDEX IF EXISTS uq_slice_jobs_design_attempt;
