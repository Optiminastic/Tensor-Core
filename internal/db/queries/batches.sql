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

-- name: ListBatchStatusesForIDs :many
-- Cheap status-only companion to a batched list-of-jobs response - the
-- pipeline_stage RESERVED/BATCHED/PRINTING signal, without a per-row
-- GetBatchByID call for every job on a list endpoint.
SELECT id, status FROM batches WHERE id = ANY(sqlc.arg('ids')::uuid[]);

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
-- plate, refreshes the snapshot metrics, marks filament reserved, and opens the
-- batch for printing. A null machine_id keeps whatever the batch worker already
-- assigned at Draft creation (see AutoCreateBatches) - an operator only needs to
-- pass one to override it. Only fires from pending_approval (Draft) - the WHERE
-- guard makes the Draft->Locked transition atomic with the filament reservation
-- the caller does in the same tx, so a lost race can never double-reserve or
-- re-approve; zero rows back means the batch was not in pending_approval.
UPDATE batches SET
    machine_id                      = COALESCE(sqlc.narg('machine_id')::uuid, machine_id),
    approved_by                     = sqlc.narg('approved_by'),
    approved_at                     = now(),
    merged_file_id                  = sqlc.narg('merged_file_id'),
    material_shortage               = sqlc.arg('material_shortage'),
    units_per_bed                   = sqlc.narg('units_per_bed'),
    total_print_time_minutes        = sqlc.narg('total_print_time_minutes'),
    effective_time_per_unit_minutes = sqlc.narg('effective_time_per_unit_minutes')::float8,
    total_filament_grams            = sqlc.narg('total_filament_grams')::float8,
    bed_utilization_percent         = sqlc.narg('bed_utilization_percent')::float8,
    filament_reserved               = true,
    status                          = 'open',
    updated_at                      = now()
WHERE id = sqlc.arg('id') AND status = 'pending_approval'
RETURNING *;

-- name: SetBatchPreviewFile :one
UPDATE batches SET preview_file_id = sqlc.arg('preview_file_id'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: UpdateBatchDerivedMetrics :one
-- Refreshes a Draft batch's computed snapshot fields after its job
-- membership changes (add/remove a job) - the same fields
-- cachePreview/buildMergedPlate already compute at creation time, just not
-- previously persisted outside creation/approval. All nullable so an
-- emptied-out batch (every job removed) clears back to unset rather than
-- keeping stale numbers.
UPDATE batches SET
    preview_file_id         = sqlc.narg('preview_file_id'),
    units_per_bed            = sqlc.narg('units_per_bed'),
    bed_utilization_percent = sqlc.narg('bed_utilization_percent')::float8,
    total_filament_grams    = sqlc.narg('total_filament_grams')::float8,
    updated_at               = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListPendingApprovalBatchesForMachine :many
-- Draft batches still parked on a machine profile that's about to go
-- offline/maintenance - reassignBatchesForOfflineMachine's candidates (see
-- machine_scheduler.go). Only pending_approval: an approved batch is a human
-- commitment and is left alone for a human to move manually.
SELECT * FROM batches WHERE machine_id = sqlc.arg('machine_id') AND status = 'pending_approval';
