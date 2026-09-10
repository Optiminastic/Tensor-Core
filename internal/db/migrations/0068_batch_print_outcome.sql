-- +goose Up
-- What actually happened to a bed on the printer, as BambuBuddy recorded it.
--
-- Until now Tensor never found out. BambuBuddy knows a plate finished - it has
-- the archive, the real duration, the real filament and the failure reason - and
-- nothing carried that back, so a batch only ever reached 'completed' when a
-- person ticked its planks in the Done dialog. Since completing a batch is the
-- ONE thing that releases its jobs to Assembly, the whole downstream board
-- waited on that person.
--
-- print_outcome is the idempotency key as much as a record. The reconciliation
-- pass runs every fleet sync and a push event can arrive twice, so the write
-- that claims a finished print is a single UPDATE guarded on this being null:
-- exactly one caller wins and the rest are no-ops. It is recorded BEFORE the
-- status change, so a crash between the two leaves evidence to repair from
-- rather than a bed that silently never completed.
--
-- Deliberately NOT a new batches.status value. The lifecycle vocabulary is
-- closed in four places (production/lifecycle.go, batchStatusTargets,
-- PipelineStage, and the frontend's BatchStatusSchema) and a failed print is not
-- a fifth state - it is "still locked, and here is why", which print_error
-- already says.
--
-- The actual_* columns sit beside total_print_time_minutes and
-- total_filament_grams rather than overwriting them: those describe what was
-- PLANNED, they are what the scheduler projects from, and ApproveBatchFor takes
-- care to preserve them. Keeping both is the only feedback loop there is on
-- whether an estimate was any good.
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS archive_id                integer,
    ADD COLUMN IF NOT EXISTS print_outcome             varchar(16),
    ADD COLUMN IF NOT EXISTS print_started_at          timestamptz,
    ADD COLUMN IF NOT EXISTS print_finished_at         timestamptz,
    ADD COLUMN IF NOT EXISTS actual_print_time_minutes integer,
    ADD COLUMN IF NOT EXISTS actual_filament_grams     numeric(10, 2);

-- Partial, because the reconciliation pass looks up in-flight beds by the
-- identifier BambuBuddy gave them, and almost every row in this table is
-- neither in flight nor ever sent.
CREATE INDEX IF NOT EXISTS ix_batches_queue_item ON batches (queue_item_id)
    WHERE queue_item_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_batches_archive ON batches (archive_id)
    WHERE archive_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_batches_archive;
DROP INDEX IF EXISTS ix_batches_queue_item;
ALTER TABLE batches
    DROP COLUMN IF EXISTS actual_filament_grams,
    DROP COLUMN IF EXISTS actual_print_time_minutes,
    DROP COLUMN IF EXISTS print_finished_at,
    DROP COLUMN IF EXISTS print_started_at,
    DROP COLUMN IF EXISTS print_outcome,
    DROP COLUMN IF EXISTS archive_id;
