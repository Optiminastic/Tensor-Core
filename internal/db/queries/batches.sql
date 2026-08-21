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
--
-- Print time is refreshed here too. It used not to be, so adding or removing a
-- job left total_print_time_minutes describing the batch's PREVIOUS contents -
-- and that column is what the machine scheduler ranks load on, so a batch could
-- be assigned on the strength of a plate that no longer existed. plate_sliced_at
-- is cleared for the same reason: whatever the plate slicer measured was a
-- different set of objects, so the batch is honestly back to an estimate.
UPDATE batches SET
    preview_file_id         = sqlc.narg('preview_file_id'),
    units_per_bed            = sqlc.narg('units_per_bed'),
    bed_utilization_percent = sqlc.narg('bed_utilization_percent')::float8,
    total_filament_grams    = sqlc.narg('total_filament_grams')::float8,
    total_print_time_minutes = sqlc.narg('total_print_time_minutes'),
    effective_time_per_unit_minutes = sqlc.narg('effective_time_per_unit_minutes')::float8,
    plate_sliced_at          = NULL,
    updated_at               = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListPendingApprovalBatchesForMachine :many
-- Draft batches still parked on a machine profile that's about to go
-- offline/maintenance - reassignBatchesForOfflineMachine's candidates (see
-- machine_scheduler.go). Only pending_approval: an approved batch is a human
-- commitment and is left alone for a human to move manually.
SELECT * FROM batches WHERE machine_id = sqlc.arg('machine_id') AND status = 'pending_approval';

-- name: NextBatchNumber :one
-- The batch counterpart of NextJobNumber - same rationale, same sequence
-- treatment. See migration 0033.
SELECT ('BATCH-' || nextval('batch_number_seq')::text)::text AS batch_number;

-- name: SetBatchPlateSliceResult :one
-- Replaces the MAX-of-jobs approximation with a real measurement of THIS bed.
-- Written only by the plate-slice worker (SliceBatchArgs), and only on success:
-- effective_time_per_unit_minutes is recomputed from the same measured total so
-- the two can never describe different slices.
UPDATE batches SET
    total_print_time_minutes        = sqlc.arg('total_print_time_minutes'),
    effective_time_per_unit_minutes = sqlc.narg('effective_time_per_unit_minutes')::float8,
    total_filament_grams            = sqlc.narg('total_filament_grams')::float8,
    total_layers                    = sqlc.narg('total_layers'),
    support_grams                   = sqlc.narg('support_grams')::float8,
    purge_grams                     = sqlc.narg('purge_grams')::float8,
    colour_changes                  = sqlc.narg('colour_changes'),
    -- COALESCE, not a plain assignment: the column is NOT NULL, so a caller
    -- that has no colour split to record would otherwise violate the
    -- constraint rather than simply leaving the existing value alone.
    filament_by_colour              = COALESCE(sqlc.narg('filament_by_colour'), filament_by_colour),
    plate_sliced_at                 = now(),
    plate_slice_error               = NULL,
    updated_at                      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: SetBatchPlateSliceError :exec
-- Records why a plate slice failed, after River gave up retrying. Deliberately
-- leaves total_print_time_minutes alone: the batchTimeFromJobs approximation is
-- wrong but present, and scheduling needs some number. plate_sliced_at stays
-- NULL, so the batch still reads as "never measured".
UPDATE batches SET plate_slice_error = sqlc.arg('plate_slice_error'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: StartBatchPrint :one
-- Claims an approved batch for a machine that is about to start printing it.
-- The `status = 'open'` guard is the whole point and mirrors ApproveBatch's
-- own: two schedulers (or two simulator ticks) racing for the same bed both
-- run this, and exactly one gets a row back. The loser sees no rows and must
-- pick a different batch rather than double-assigning one plate to two beds.
--
-- Deliberately separate from the permissive UpdateBatch rather than tightening
-- it: the Kanban board drags batches between columns in both directions and
-- depends on UpdateBatch accepting any transition.
UPDATE batches SET status = 'in_progress', updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'open'
RETURNING *;

-- name: ListDraftBatchIDs :many
-- Every Draft batch, for the planner to dissolve and reform.
SELECT id FROM batches WHERE status = 'pending_approval' ORDER BY created_at ASC, id ASC;

-- name: DeleteDraftBatches :exec
-- Removes dissolved Drafts. The status predicate is the real guard, not
-- decoration: it makes a stale or wrong id list incapable of deleting an
-- approved batch, which would strand a plate a machine is about to print.
DELETE FROM batches WHERE id = ANY(sqlc.arg('batch_ids')::uuid[]) AND status = 'pending_approval';

-- name: CountCommittedBatchesForMachine :one
-- How many batches are already committed to a machine profile: approved and
-- waiting ('open') plus whatever it is printing ('in_progress').
--
-- This is what bounds the locked queue. A committed batch is frozen - it can no
-- longer absorb a newly-arrived compatible job, and re-planning will not touch
-- it - so committing more than the immediate next plate converts future
-- flexibility into a fixed schedule for no gain. One next-up keeps the machine
-- fed while everything beyond it stays an editable Draft.
SELECT count(*) FROM batches
WHERE machine_id = sqlc.arg('machine_id')
  AND status IN ('open', 'in_progress');

-- name: ListApprovableDraftsForMachine :many
-- Draft batches assigned to a machine profile, most ready first, for promotion
-- into the locked queue.
--
-- Order is the planner's own preference: fuller beds first, then
-- longest-waiting, so promotion picks what the optimizer would have picked.
--
-- This used to lead with `(plate_sliced_at IS NOT NULL) DESC`, from when Drafts
-- were sliced on creation and an unsliced one could not be committed. Slicing
-- now happens AT approval, so no Draft ever carries plate_sliced_at and the
-- term was dead - always false for every row, sorting nothing. Removed rather
-- than left in place: an ordering rule that cannot fire reads as a rule that
-- matters, and the next person to touch this would have to re-derive that it
-- does not.
SELECT * FROM batches
WHERE machine_id = sqlc.arg('machine_id')
  AND status = 'pending_approval'
ORDER BY bed_utilization_percent DESC NULLS LAST,
         created_at ASC
LIMIT sqlc.arg('row_limit');

-- name: CountDraftBatchesPerMachine :many
-- The flexible, not-yet-committed work pointed at each machine profile: how
-- many Draft batches, and how many minutes of printing they represent.
--
-- Minutes, not just a count, because that is what fleet balance actually turns
-- on - eight hours of Drafts on one machine and one hour on another are not
-- equivalent loads however many batches each is split into. Drafts are excluded
-- from MachineFreeAt (which carries only guaranteed work), so without this every
-- Draft in a planning run sees identically-loaded machines and they all land on
-- whichever sorts first.
--
-- COALESCE because a Draft whose plate has not been sliced yet has no estimate;
-- it is charged nothing rather than excluding the machine from the sum.
SELECT machine_id,
       count(*) AS draft_count,
       COALESCE(sum(total_print_time_minutes), 0)::bigint AS draft_minutes
FROM batches
WHERE status = 'pending_approval' AND machine_id IS NOT NULL
GROUP BY machine_id;

-- name: ListDraftBatchJobIDs :many
-- Every Draft batch with the job ids currently on it, for deciding which
-- Drafts a new plan actually changes. A Draft whose job set is unchanged is
-- kept rather than deleted and recreated - see AutoCreateBatches.
SELECT b.id AS batch_id, j.id AS job_id
FROM batches b
JOIN production_jobs j ON j.batch_id = b.id
WHERE b.status = 'pending_approval'
ORDER BY b.id, j.id;
