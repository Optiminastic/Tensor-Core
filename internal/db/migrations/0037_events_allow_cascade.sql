-- Lets a job's history be removed along with the job, while keeping the log
-- append-only against everything else.
--
-- Migration 0035 gave production_job_events a BEFORE UPDATE OR DELETE trigger
-- that raises unconditionally, and a job_id foreign key declared ON DELETE
-- CASCADE. Those two contradict each other: the cascade tries to delete the
-- child rows, the trigger refuses, and the whole delete aborts with
-- "production_job_events is append-only (attempted DELETE)".
--
-- The consequence was larger than it looks. NO production job that had ever
-- recorded an event could be deleted at all - which silently broke every
-- cleanup path that exists: cmd/simulator's -reset, cmd/seedreal's
-- resetSeedData, and any future job removal. The append-only guarantee was
-- protecting the log by making the rows it described immortal.
--
-- The fix distinguishes the two cases. For ON DELETE CASCADE, Postgres removes
-- the parent row first and then fires the child delete, so by the time this
-- trigger runs the referenced job is already gone. A DELETE whose job still
-- exists is therefore someone editing history directly, and is still refused.
-- UPDATE remains refused outright - there is no legitimate reason to rewrite
-- an event.
--
-- What this preserves: history cannot be altered, and cannot be selectively
-- erased for a job that is still in the system. What it allows: deleting a job
-- takes its history with it, which is the only sense in which that history was
-- ever meaningful.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION production_job_events_append_only() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (
        SELECT 1 FROM production_jobs WHERE id = OLD.job_id
    ) THEN
        -- The job itself is being deleted; let its history go with it.
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'production_job_events is append-only (attempted % while its job still exists)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION production_job_events_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'production_job_events is append-only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
