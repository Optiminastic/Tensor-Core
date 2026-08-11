-- +goose Up
-- Polishing is a post-print finishing station sitting between assembly and QC:
-- sanding, seam cleanup, coating. It mirrors assembly exactly - a per-job
-- sub-status plus an audit row per check - because it has the same shape: an
-- operator either does it and records what they did, or marks it not required
-- for a part that needs no finishing. QC then refuses to run until polishing
-- is out of 'pending', the same gate assembly already has.
--
-- Existing rows default to 'pending', which is correct for jobs still in
-- flight. Jobs already past QC are unaffected: nothing reads polishing_status
-- once qc_status has been decided.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS polishing_status varchar(32) NOT NULL DEFAULT 'pending';

CREATE TABLE IF NOT EXISTS production_job_polishing_checks (
    id                uuid PRIMARY KEY,
    job_id            uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    supports_removed  boolean NOT NULL DEFAULT false,
    sanded            boolean NOT NULL DEFAULT false,
    seams_cleaned     boolean NOT NULL DEFAULT false,
    surface_finish_ok boolean NOT NULL DEFAULT false,
    photo_file_id     uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    notes             varchar(1000),
    polished_by       varchar(64) NOT NULL,
    polished_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_polishing_checks_job_id ON production_job_polishing_checks (job_id);

-- +goose Down
DROP INDEX IF EXISTS ix_polishing_checks_job_id;
DROP TABLE IF EXISTS production_job_polishing_checks;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS polishing_status;
