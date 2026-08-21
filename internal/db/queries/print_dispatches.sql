-- name: InsertPrintDispatch :one
-- Opens a dispatch for a batch. The partial unique index on (batch_id) for live
-- statuses makes a duplicate a 23505 rather than a second print of the same plate.
INSERT INTO print_dispatches (id, batch_id, printer_id, status)
VALUES (sqlc.arg('id'), sqlc.arg('batch_id'), sqlc.arg('printer_id'), 'pending')
RETURNING *;

-- name: GetPrintDispatch :one
SELECT * FROM print_dispatches WHERE id = sqlc.arg('id');

-- name: MarkPrintDispatchQueued :one
-- The plate is uploaded and sitting in BamBuddy's queue. With manual_start it
-- waits for a person; otherwise the scheduler takes it from here.
UPDATE print_dispatches SET
    library_file_id  = sqlc.arg('library_file_id'),
    queue_item_id    = sqlc.arg('queue_item_id'),
    filament_warning = sqlc.narg('filament_warning'),
    status           = 'queued',
    error            = NULL,
    dispatched_at    = now(),
    updated_at       = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: MarkPrintDispatchPrinting :exec
UPDATE print_dispatches SET
    status     = 'printing',
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: MarkPrintDispatchCompleted :exec
UPDATE print_dispatches SET
    status       = 'completed',
    completed_at = now(),
    updated_at   = now()
WHERE id = sqlc.arg('id');

-- name: FailPrintDispatch :exec
-- Terminal failure. Clearing the live status also releases the partial unique
-- index, so the batch can be dispatched again once the cause is fixed.
UPDATE print_dispatches SET
    status       = 'failed',
    error        = sqlc.arg('error'),
    completed_at = now(),
    updated_at   = now()
WHERE id = sqlc.arg('id');

-- name: ListOpenPrintDispatches :many
-- Everything still worth asking BamBuddy about. The poller idles when empty.
SELECT * FROM print_dispatches
WHERE status IN ('pending', 'queued', 'printing')
ORDER BY created_at;

-- name: ListPrintDispatchesForBatch :many
SELECT * FROM print_dispatches WHERE batch_id = sqlc.arg('batch_id')
ORDER BY created_at DESC;

-- name: GetFleetMachineForProfile :one
-- Resolves the machine a batch was approved against (a machine_profiles row) to
-- the physical unit that will print it, and thus to its BamBuddy printer.
SELECT * FROM machines
WHERE machine_profile_id = sqlc.arg('machine_profile_id')
  AND bambuddy_printer_id IS NOT NULL
ORDER BY created_at
LIMIT 1;

-- name: SetFleetMachineBambuddyPrinter :one
-- Links a fleet machine to its BamBuddy printer and slicing profile.
UPDATE machines SET
    bambuddy_printer_id = sqlc.narg('bambuddy_printer_id'),
    machine_profile_id  = sqlc.narg('machine_profile_id'),
    updated_at          = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: GetFleetMachineByBambuddyPrinter :one
-- The physical unit behind a BamBuddy printer, so live print state can be written
-- back onto the fleet row the dashboard already renders.
SELECT * FROM machines WHERE bambuddy_printer_id = sqlc.arg('bambuddy_printer_id');
