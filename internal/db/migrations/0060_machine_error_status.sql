-- +goose Up
-- Let a machine say it is broken.
--
-- 0017 pinned the fleet's status vocabulary to (idle, running, off). Adding
-- 'error' to the application without widening this would fail the sync at
-- exactly the moment it matters: the upsert for the one machine that had just
-- faulted would violate the check and abort, so a broken printer would keep
-- reporting its last healthy status. An integration test caught this.
--
-- A separate migration from 0059 rather than an edit to it: 0059 is already
-- recorded as applied on the shared database, and goose never re-runs an
-- applied version - editing it would fix only databases created afterwards.
--
-- Postgres cannot widen a CHECK in place, so it is dropped and recreated. The
-- constraint name is the one Postgres generated for 0017's inline CHECK.
ALTER TABLE machines DROP CONSTRAINT IF EXISTS machines_status_check;
ALTER TABLE machines ADD CONSTRAINT machines_status_check
    CHECK (status IN ('idle', 'running', 'off', 'error'));

-- +goose Down
-- Any machine currently in 'error' has to move before the narrower constraint
-- can go back on, or the rollback itself fails. 'off' is the honest landing
-- place: the row can no longer say what is wrong with it.
UPDATE machines SET status = 'off' WHERE status = 'error';
ALTER TABLE machines DROP CONSTRAINT IF EXISTS machines_status_check;
ALTER TABLE machines ADD CONSTRAINT machines_status_check
    CHECK (status IN ('idle', 'running', 'off'));
