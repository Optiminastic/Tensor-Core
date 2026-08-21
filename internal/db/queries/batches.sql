-- Batches: jobs grouped onto one machine bed. Nullable numeric snapshot columns
-- are selected raw (pgtype.Numeric) and mapped with db.NumFloatPtr; input params
-- are cast to float8 so handlers bind float64.

-- name: InsertBatch :one
INSERT INTO batches (
    id, batch_number, machine_id, status, material_shortage, units_per_bed,
    total_print_time_minutes, effective_time_per_unit_minutes, total_filament_grams,
    bed_utilization_percent, packing_strategy
) VALUES (
    sqlc.arg('id'), sqlc.arg('batch_number'), sqlc.narg('machine_id'), sqlc.arg('status'),
    sqlc.arg('material_shortage'), sqlc.narg('units_per_bed'), sqlc.narg('total_print_time_minutes'),
    sqlc.narg('effective_time_per_unit_minutes')::float8, sqlc.narg('total_filament_grams')::float8,
    sqlc.narg('bed_utilization_percent')::float8, sqlc.narg('packing_strategy')
)
RETURNING *;

-- name: GetBatchByID :one
SELECT * FROM batches WHERE id = $1;

-- name: ListBatches :many
SELECT * FROM batches ORDER BY created_at DESC, id DESC;

-- name: ListBatchesPage :many
SELECT * FROM batches
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: UpdateBatch :one
-- Status and machine reassignment; machine_id uses a set-flag so it can be cleared.
UPDATE batches SET
    status     = COALESCE(sqlc.narg('status'), status),
    machine_id = CASE WHEN sqlc.arg('set_machine_id')::bool THEN sqlc.narg('machine_id') ELSE machine_id END,
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ApproveBatch :one
-- Records the approval: assigns the machine, stamps approver/time, links the merged
-- plate, refreshes the snapshot metrics, and opens the batch for printing.
UPDATE batches SET
    machine_id                      = sqlc.arg('machine_id'),
    approved_by                     = sqlc.narg('approved_by'),
    approved_at                     = now(),
    merged_file_id                  = sqlc.narg('merged_file_id'),
    material_shortage               = sqlc.arg('material_shortage'),
    units_per_bed                   = sqlc.narg('units_per_bed'),
    total_print_time_minutes        = sqlc.narg('total_print_time_minutes'),
    effective_time_per_unit_minutes = sqlc.narg('effective_time_per_unit_minutes')::float8,
    total_filament_grams            = sqlc.narg('total_filament_grams')::float8,
    bed_utilization_percent         = sqlc.narg('bed_utilization_percent')::float8,
    status                          = 'open',
    updated_at                      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: SetBatchPreviewFile :one
UPDATE batches SET preview_file_id = sqlc.arg('preview_file_id'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: MarkBatchSlicing :exec
-- Claims the plate slice for a batch, so a retry or a duplicate trigger can tell
-- "already running" from "never started".
UPDATE batches SET
    slice_status = 'running',
    slice_error  = NULL,
    updated_at   = now()
WHERE id = sqlc.arg('id');

-- name: SetBatchGcode :one
-- Records the sliced plate: the object key a printer can actually consume.
UPDATE batches SET
    gcode_key    = sqlc.arg('gcode_key'),
    sliced_at    = now(),
    slice_status = 'done',
    slice_error  = NULL,
    updated_at   = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: FailBatchSlice :exec
-- Records why a plate slice failed, so the batch shows a reason instead of just
-- never gaining a gcode_key.
UPDATE batches SET
    slice_status = 'failed',
    slice_error  = sqlc.arg('slice_error'),
    updated_at   = now()
WHERE id = sqlc.arg('id');
