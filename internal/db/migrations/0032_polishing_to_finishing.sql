-- +goose Up
-- The station added in 0031 as "polishing" is called "finishing" in the
-- production spec - sanding, seam cleanup, coating between assembly and QC.
-- Renaming rather than leaving the domain layer on a different word: this
-- service owns the domain vocabulary, and a split name becomes permanent
-- tribal knowledge. Every statement here is catalog-only (instant, no table
-- rewrite), so this is cheap despite touching a hot table.
--
-- NOTE: the permission key changes too, so `cmd/seed` must be re-run and every
-- user's permissions_version bumped - a JWT minted before this migration still
-- carries polishing:submit and will 403 against the finishing:submit guard.
ALTER TABLE production_jobs RENAME COLUMN polishing_status TO finishing_status;

ALTER TABLE production_job_polishing_checks RENAME TO production_job_finishing_checks;
ALTER TABLE production_job_finishing_checks RENAME COLUMN polished_by TO finished_by;
ALTER TABLE production_job_finishing_checks RENAME COLUMN polished_at TO finished_at;
ALTER INDEX ix_polishing_checks_job_id RENAME TO ix_finishing_checks_job_id;

UPDATE permissions SET resource = 'finishing' WHERE resource = 'polishing';

-- +goose Down
UPDATE permissions SET resource = 'polishing' WHERE resource = 'finishing';

ALTER INDEX ix_finishing_checks_job_id RENAME TO ix_polishing_checks_job_id;
ALTER TABLE production_job_finishing_checks RENAME COLUMN finished_at TO polished_at;
ALTER TABLE production_job_finishing_checks RENAME COLUMN finished_by TO polished_by;
ALTER TABLE production_job_finishing_checks RENAME TO production_job_polishing_checks;

ALTER TABLE production_jobs RENAME COLUMN finishing_status TO polishing_status;
