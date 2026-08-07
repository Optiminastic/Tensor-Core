-- +goose Up
-- Slicing-config fields for machine_profiles, exposed via /machines (distinct
-- from /config/machines' cost-only view - see internal/httpapi/machines_ops.go
-- vs config.go). These columns already exist on the shared dev database
-- (added directly, untracked) with these exact types; this migration is a
-- no-op there and creates the same shape on any fresh database. right_nozzle_mm
-- is new (the untracked version only had a single nozzle_mm) for dual-nozzle
-- printers.
ALTER TABLE machine_profiles
    ADD COLUMN IF NOT EXISTS family varchar(16) NOT NULL DEFAULT 'H2S',
    ADD COLUMN IF NOT EXISTS nozzle_mm numeric(3, 2) NOT NULL DEFAULT 0.4,
    ADD COLUMN IF NOT EXISTS right_nozzle_mm numeric(3, 2),
    ADD COLUMN IF NOT EXISTS flow varchar(16) NOT NULL DEFAULT 'standard',
    ADD COLUMN IF NOT EXISTS default_colour varchar(40),
    ADD COLUMN IF NOT EXISTS layer_height_min_mm numeric(4, 2) NOT NULL DEFAULT 0.08,
    ADD COLUMN IF NOT EXISTS layer_height_max_mm numeric(4, 2) NOT NULL DEFAULT 0.28,
    ADD COLUMN IF NOT EXISTS supported_filaments jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS uq_machine_profiles_default ON machine_profiles (is_default) WHERE is_default;

-- Also untracked drift on the shared dev database, also a no-op there: which
-- printer profile a design was sliced for. The job-creation worker follows
-- this to snapshot nozzle/quality facts onto a matched job.
ALTER TABLE designs ADD COLUMN IF NOT EXISTS machine_id uuid REFERENCES machine_profiles (id) ON DELETE SET NULL;

-- Per-job snapshots of the printer profile and slice facts a job was matched
-- to, plus the multi-colour set and the compatibility-grouping dimensions
-- (support/infill) the planner needs. issue_reason is the validation stage's
-- fixed reason taxonomy (internal/production/lifecycle.go) - null means no
-- issue.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS colours jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS support_used boolean,
    ADD COLUMN IF NOT EXISTS infill_pct numeric(5, 2),
    ADD COLUMN IF NOT EXISTS left_nozzle_mm numeric(3, 2),
    ADD COLUMN IF NOT EXISTS right_nozzle_mm numeric(3, 2),
    ADD COLUMN IF NOT EXISTS flow_pct numeric(6, 2),
    ADD COLUMN IF NOT EXISTS quality_mm numeric(4, 3),
    ADD COLUMN IF NOT EXISTS issue_reason varchar(32);
CREATE INDEX IF NOT EXISTS ix_production_jobs_colours ON production_jobs USING gin (colours);
CREATE INDEX IF NOT EXISTS ix_production_jobs_issue ON production_jobs (issue_reason) WHERE issue_reason IS NOT NULL;

-- +goose Down
ALTER TABLE production_jobs
    DROP COLUMN IF EXISTS issue_reason,
    DROP COLUMN IF EXISTS quality_mm,
    DROP COLUMN IF EXISTS flow_pct,
    DROP COLUMN IF EXISTS right_nozzle_mm,
    DROP COLUMN IF EXISTS left_nozzle_mm,
    DROP COLUMN IF EXISTS infill_pct,
    DROP COLUMN IF EXISTS support_used,
    DROP COLUMN IF EXISTS colours;
ALTER TABLE designs DROP COLUMN IF EXISTS machine_id;
DROP INDEX IF EXISTS uq_machine_profiles_default;
ALTER TABLE machine_profiles
    DROP COLUMN IF EXISTS is_default,
    DROP COLUMN IF EXISTS supported_filaments,
    DROP COLUMN IF EXISTS layer_height_max_mm,
    DROP COLUMN IF EXISTS layer_height_min_mm,
    DROP COLUMN IF EXISTS default_colour,
    DROP COLUMN IF EXISTS flow,
    DROP COLUMN IF EXISTS right_nozzle_mm,
    DROP COLUMN IF EXISTS nozzle_mm,
    DROP COLUMN IF EXISTS family;
