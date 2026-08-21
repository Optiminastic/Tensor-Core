-- Sending a sliced batch plate to a printer, via BamBuddy on the shop LAN.
--
-- print_dispatches is Tensor's own record of that hand-off. It exists because the
-- integration has state the batch does not: which BamBuddy library file and queue
-- item a plate became, and how far that queue item has got. Keeping it in its own
-- table means a re-dispatch (after a failure, or a reprint) is a new row rather
-- than an overwrite, so the history of what was sent to which printer survives.
--
-- The machines columns resolve a long-standing ambiguity. batches.machine_id
-- points at machine_profiles (WHICH CONFIG to slice with), while machines is the
-- physical fleet (WHICH UNIT is printing). Nothing linked them, so the fleet table
-- was a dead end. machine_profile_id bridges the two, and bambuddy_printer_id maps
-- a physical unit to its BamBuddy printer - rather than adding a second, competing
-- machine column to batches.

-- +goose Up
ALTER TABLE machines ADD COLUMN IF NOT EXISTS machine_profile_id uuid
    REFERENCES machine_profiles (id) ON DELETE SET NULL;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS bambuddy_printer_id integer;

CREATE INDEX IF NOT EXISTS idx_machines_machine_profile_id ON machines (machine_profile_id);
-- One physical unit per BamBuddy printer: two fleet rows claiming the same
-- printer would let two batches believe they own the same machine.
CREATE UNIQUE INDEX IF NOT EXISTS uq_machines_bambuddy_printer_id
    ON machines (bambuddy_printer_id) WHERE bambuddy_printer_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS print_dispatches (
    id                  uuid PRIMARY KEY,
    batch_id            uuid NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    -- BamBuddy-side identifiers. printer_id is known up front; the other two are
    -- filled in as the hand-off progresses, so a failure halfway is legible.
    printer_id          integer NOT NULL,
    library_file_id     integer,
    queue_item_id       integer,
    status              varchar(32) NOT NULL DEFAULT 'pending',
    error               text,
    -- A filament mismatch that did not stop the dispatch (the plate was staged
    -- anyway). Recorded so the operator sees it before pressing start.
    filament_warning    text,
    dispatched_at       timestamptz,
    started_at          timestamptz,
    completed_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_print_dispatches_batch ON print_dispatches (batch_id);
-- Polling reads exactly this: the dispatches still worth asking BamBuddy about.
CREATE INDEX IF NOT EXISTS ix_print_dispatches_open ON print_dispatches (status)
    WHERE status IN ('pending', 'queued', 'printing');
-- At most one live dispatch per batch, so a duplicate trigger or a webhook replay
-- cannot put the same plate on a printer twice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_print_dispatches_open_batch
    ON print_dispatches (batch_id) WHERE status IN ('pending', 'queued', 'printing');

-- +goose Down
DROP TABLE IF EXISTS print_dispatches;
DROP INDEX IF EXISTS uq_machines_bambuddy_printer_id;
DROP INDEX IF EXISTS idx_machines_machine_profile_id;
ALTER TABLE machines DROP COLUMN IF EXISTS bambuddy_printer_id;
ALTER TABLE machines DROP COLUMN IF EXISTS machine_profile_id;
