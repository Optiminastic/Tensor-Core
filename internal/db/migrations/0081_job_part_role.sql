-- +goose Up
-- Which of its product's design files a job prints.
--
-- A job has always been one printed thing: one model in print_file_id, one
-- place on a bed, one colour, one QC check. A Soulmate Combo is three printed
-- things sold as one line - a plank, a rose and a keychain - and it was
-- arriving as ONE job that rendered only the plank. JOB-115257 carried
-- "Name On 3D Rose" and "Name On Heart Keychain" and printed neither.
--
-- The fix is not a job that holds three models - that would break batching,
-- bed packing and per-part colour, all of which are per job for good reasons.
-- It is three jobs, which is also what is physically true: the plank and the
-- rose are different objects, in different filament, on different beds.
--
-- This column is what tells one of those jobs from another. 'body' is the
-- default and every job that exists is one, so nothing changes until job
-- creation starts emitting more than one job per line.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS part_role varchar(24) NOT NULL DEFAULT 'body';

-- +goose Down
ALTER TABLE production_jobs DROP COLUMN IF EXISTS part_role;
