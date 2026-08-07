-- +goose Up
-- Snapshot of the matched printer profile's family (e.g. "H2S", "H2C") - which
-- printer type this job must run on. Missed in 0022; the planner's grouping
-- key needs it alongside the nozzle/quality snapshots added there.
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS machine_family varchar(16);

-- +goose Down
ALTER TABLE production_jobs DROP COLUMN IF EXISTS machine_family;
