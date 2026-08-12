-- +goose Up
-- job_number and batch_number were 5 random digits (production.newNumber) with
-- no unique index, so a collision was possible and would have been accepted
-- silently. The space is only 100k: by ~500 jobs the probability of at least one
-- duplicate is already around 70%.
--
-- The repair runs first and is deterministic - suffix by row_number within each
-- duplicate group, oldest keeping the original number. Not a re-roll: a random
-- repair can collide with itself or with an untouched row, and would need a
-- retry loop inside a migration. 'JOB-12345-D2' is a shape the old minter could
-- never emit, so a repaired value cannot collide with anything. Both columns are
-- varchar(64) and the longest existing value is 11 characters, so there is ample
-- room. On a database with no duplicates (the expected case) both statements
-- update zero rows.
--
-- Down does not un-repair: the pre-repair state was corrupt, and nothing records
-- which row originally held which duplicate.
WITH ranked AS (
    SELECT id, job_number,
           row_number() OVER (PARTITION BY job_number ORDER BY created_at, id) AS rn
    FROM production_jobs
)
UPDATE production_jobs p
SET job_number = r.job_number || '-D' || r.rn, updated_at = now()
FROM ranked r
WHERE p.id = r.id AND r.rn > 1;

WITH ranked AS (
    SELECT id, batch_number,
           row_number() OVER (PARTITION BY batch_number ORDER BY created_at, id) AS rn
    FROM batches
)
UPDATE batches b
SET batch_number = r.batch_number || '-D' || r.rn, updated_at = now()
FROM ranked r
WHERE b.id = r.id AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_jobs_job_number
    ON production_jobs (job_number);
CREATE UNIQUE INDEX IF NOT EXISTS uq_batches_batch_number
    ON batches (batch_number);

-- Minting moves from random digits to a sequence, shipped WITH the index
-- because the index alone would turn a silent collision into a 500 on every
-- create path. Starting at 1,000,000 means every minted value is 7 digits and
-- can never collide with a legacy 5-digit number or a repaired '-D2' one.
CREATE SEQUENCE IF NOT EXISTS production_job_number_seq START WITH 1000000;
CREATE SEQUENCE IF NOT EXISTS batch_number_seq START WITH 1000000;

-- +goose Down
DROP SEQUENCE IF EXISTS batch_number_seq;
DROP SEQUENCE IF EXISTS production_job_number_seq;
DROP INDEX IF EXISTS uq_batches_batch_number;
DROP INDEX IF EXISTS uq_production_jobs_job_number;
