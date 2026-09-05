-- +goose Up
-- Why a generated model could not be built.
--
-- A Dual Name Plank renders itself from the customer's names. When that fails -
-- a name OpenSCAD cannot set, a template fault, a missing font - the job was
-- left looking identical to one whose render simply had not run yet: both had
-- no print file and both read as "in progress". The operator had no way to tell
-- a plank being made from one that never would be.
--
-- Recorded here rather than in issue_reason, which is a 32-character enum the
-- batch planner switches on and cannot carry a sentence.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS model_error    text,
    ADD COLUMN IF NOT EXISTS model_error_at timestamptz;

-- +goose Down
ALTER TABLE production_jobs
    DROP COLUMN IF EXISTS model_error,
    DROP COLUMN IF EXISTS model_error_at;
