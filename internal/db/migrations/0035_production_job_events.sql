-- +goose Up
-- One append-only stream per job: created -> batched -> printed -> assembly ->
-- finishing -> QC -> packaging -> dispatched, including every issue raised and
-- every reprint requested along the way.
--
-- One generic table rather than per-stage tables. The requirement is a single
-- ordered history, which per-stage tables can only answer with a multi-way
-- UNION and a re-sort at read time - and that is precisely why the four
-- existing check tables (assembly/finishing/qc/packaging) have never been read
-- by anything: they are insert-only and no endpoint returns them. Those stay as
-- they are, holding the typed per-station checklists; each station now also
-- writes one event row here, with the check row's id in metadata so the two can
-- be joined when a UI wants the detail.
--
-- seq is a bigserial rather than relying on created_at: two events written in
-- the same transaction can share a timestamp to the microsecond, and a timeline
-- that reorders itself is worse than no timeline.
CREATE TABLE IF NOT EXISTS production_job_events (
    id             uuid PRIMARY KEY,
    job_id         uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    seq            bigserial NOT NULL,
    event_type     varchar(48) NOT NULL,
    stage          varchar(24),
    reason         varchar(64),
    comment        varchar(1000),
    actor_id       varchar(64) NOT NULL,
    batch_id       uuid REFERENCES batches (id) ON DELETE SET NULL,
    related_job_id uuid REFERENCES production_jobs (id) ON DELETE SET NULL,
    metadata       jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_job_events_job ON production_job_events (job_id, seq);
CREATE INDEX IF NOT EXISTS ix_job_events_type ON production_job_events (event_type);

-- Append-only is enforced here, not just by convention. No UPDATE or DELETE
-- query is written against this table, so sqlc cannot generate one - but that
-- only holds until someone adds a query. This survives that.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION production_job_events_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'production_job_events is append-only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_production_job_events_append_only ON production_job_events;
CREATE TRIGGER trg_production_job_events_append_only
    BEFORE UPDATE OR DELETE ON production_job_events
    FOR EACH ROW EXECUTE FUNCTION production_job_events_append_only();

-- Backfill from what actually has a timestamp and an actor. Deliberately NOT
-- backfilled: batched, printing and printed. Nothing ever recorded when those
-- happened, so historical jobs will show a gap between creation and assembly.
-- That gap is honest; inventing timestamps would not be.
INSERT INTO production_job_events (id, job_id, event_type, stage, actor_id, batch_id, created_at)
SELECT gen_random_uuid(), j.id, 'job.created', NULL, 'system', j.batch_id, j.created_at
FROM production_jobs j;

INSERT INTO production_job_events (id, job_id, event_type, stage, comment, actor_id, metadata, created_at)
SELECT gen_random_uuid(), c.job_id, 'assembly.completed', 'assembly', c.notes,
       c.assembled_by, jsonb_build_object('check_id', c.id), c.assembled_at
FROM production_job_assembly_checks c;

INSERT INTO production_job_events (id, job_id, event_type, stage, comment, actor_id, metadata, created_at)
SELECT gen_random_uuid(), c.job_id, 'finishing.completed', 'finishing', c.notes,
       c.finished_by, jsonb_build_object('check_id', c.id), c.finished_at
FROM production_job_finishing_checks c;

INSERT INTO production_job_events (id, job_id, event_type, stage, comment, actor_id, metadata, created_at)
SELECT gen_random_uuid(), c.job_id,
       CASE WHEN c.decision = 'pass' THEN 'qc.passed' ELSE 'qc.failed' END, 'qc', c.notes,
       c.inspected_by, jsonb_build_object('check_id', c.id), c.inspected_at
FROM production_job_qc_checks c;

INSERT INTO production_job_events (id, job_id, event_type, stage, actor_id, metadata, created_at)
SELECT gen_random_uuid(), p.job_id, 'packaging.packed', 'packaging', p.packed_by,
       jsonb_build_object('packaging_type', p.packaging_type), p.packed_at
FROM production_job_packaging_details p;

INSERT INTO production_job_events (id, job_id, event_type, stage, reason, comment, actor_id, created_at)
SELECT gen_random_uuid(), f.job_id, 'print.failed', f.stage, f.reason, f.notes,
       f.created_by, f.created_at
FROM production_job_failures f;

-- A reprint is recorded against the ORIGINAL job - that is where someone looks
-- when asking "what happened to this product".
INSERT INTO production_job_events (id, job_id, event_type, actor_id, related_job_id, created_at)
SELECT gen_random_uuid(), r.reprint_of_job_id, 'reprint.created', 'system', r.id, r.created_at
FROM production_jobs r
WHERE r.reprint_of_job_id IS NOT NULL;

-- +goose Down
DROP TRIGGER IF EXISTS trg_production_job_events_append_only ON production_job_events;
DROP FUNCTION IF EXISTS production_job_events_append_only();
DROP TABLE IF EXISTS production_job_events;
