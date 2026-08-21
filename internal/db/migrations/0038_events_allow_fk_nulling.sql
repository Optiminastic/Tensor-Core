-- Completes 0037. That migration let a job's history be deleted along with the
-- job, but production_job_events has two MORE foreign keys whose referential
-- actions the append-only trigger was still blocking:
--
--   batch_id       uuid REFERENCES batches (id)         ON DELETE SET NULL
--   related_job_id uuid REFERENCES production_jobs (id) ON DELETE SET NULL
--
-- "SET NULL" is an UPDATE, so deleting a batch - or deleting the reprint a
-- failure event points at - raised "production_job_events is append-only
-- (attempted UPDATE)" and aborted the whole delete. Between this and 0037,
-- neither a job nor a batch with any history could be removed at all.
--
-- The rule below allows exactly one kind of UPDATE: one that nulls those two
-- reference columns and changes nothing else, and only when the row being
-- pointed at is genuinely gone. Every field that carries the actual history -
-- who did what, when, why - is compared and must be identical, so this cannot
-- be used to rewrite an event. Any other UPDATE is still refused outright.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION production_job_events_append_only() RETURNS trigger AS $$
BEGIN
    -- The event's own job is being deleted: its history goes with it (0037).
    IF TG_OP = 'DELETE' AND NOT EXISTS (
        SELECT 1 FROM production_jobs WHERE id = OLD.job_id
    ) THEN
        RETURN OLD;
    END IF;

    -- A referenced batch or related job was deleted, and the FK is nulling the
    -- pointer to it. Permitted only when the payload is byte-identical and the
    -- referenced rows really are gone.
    IF TG_OP = 'UPDATE'
       AND NEW.id           IS NOT DISTINCT FROM OLD.id
       AND NEW.seq          IS NOT DISTINCT FROM OLD.seq
       AND NEW.job_id       IS NOT DISTINCT FROM OLD.job_id
       AND NEW.event_type   IS NOT DISTINCT FROM OLD.event_type
       AND NEW.stage        IS NOT DISTINCT FROM OLD.stage
       AND NEW.reason       IS NOT DISTINCT FROM OLD.reason
       AND NEW.comment      IS NOT DISTINCT FROM OLD.comment
       AND NEW.actor_id     IS NOT DISTINCT FROM OLD.actor_id
       AND NEW.metadata     IS NOT DISTINCT FROM OLD.metadata
       AND NEW.created_at   IS NOT DISTINCT FROM OLD.created_at
       -- batch_id may only go non-null -> null, and only if that batch is gone.
       AND (NEW.batch_id IS NOT DISTINCT FROM OLD.batch_id
            OR (NEW.batch_id IS NULL AND OLD.batch_id IS NOT NULL
                AND NOT EXISTS (SELECT 1 FROM batches WHERE id = OLD.batch_id)))
       -- likewise related_job_id.
       AND (NEW.related_job_id IS NOT DISTINCT FROM OLD.related_job_id
            OR (NEW.related_job_id IS NULL AND OLD.related_job_id IS NOT NULL
                AND NOT EXISTS (SELECT 1 FROM production_jobs WHERE id = OLD.related_job_id)))
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'production_job_events is append-only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION production_job_events_append_only() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (
        SELECT 1 FROM production_jobs WHERE id = OLD.job_id
    ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'production_job_events is append-only (attempted % while its job still exists)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
