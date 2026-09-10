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
-- Newest batch first, by the batch's own number.
--
-- created_at alone shuffled the list. batch_number comes from a sequence, so it
-- already IS creation order - but a planning run inserts every batch it makes in
-- one transaction, so dozens share a created_at to the microsecond and the rows
-- came back 1001103, 1001087, 1001084, 1001089. The number is what an operator
-- reads off the batch, so a list that does not descend by it reads as broken.
--
-- Sorted on the numeric part rather than the string: "BATCH-999" sorts after
-- "BATCH-1001051" lexically, which is only right today because every live number
-- is the same width. NULLS LAST keeps a hand-named batch (no digits at all) out
-- of the way rather than at the top.
SELECT b.* FROM batches b
-- Same box as ListBatchesPage; see there for why this reaches through the jobs.
WHERE (
    sqlc.narg('search')::text IS NULL
    OR b.batch_number ILIKE '%' || sqlc.narg('search')::text || '%'
    OR EXISTS (
        SELECT 1 FROM production_jobs j
        LEFT JOIN orders o ON o.id = j.order_id
        WHERE j.batch_id = b.id
          AND (
            j.job_number ILIKE '%' || sqlc.narg('search')::text || '%'
            OR o.order_number ILIKE '%' || sqlc.narg('search')::text || '%'
            OR j.personalisation_name ILIKE '%' || sqlc.narg('search')::text || '%'
          )
    )
)
ORDER BY NULLIF(regexp_replace(b.batch_number, '\D', '', 'g'), '')::bigint DESC NULLS LAST,
         b.created_at DESC, b.id DESC;

-- name: ListBatchesPage :many
-- Optionally narrowed to the beds carrying one order.
--
-- "Which bed is order 114873 on?" is the question the floor actually asks, and
-- it could only be answered by opening beds one at a time. Matched through the
-- bed's JOBS rather than on the batch itself, because a batch records nothing
-- about which orders it holds - job_number carries the order number ("JOB-114873")
-- and the join reaches the order's own number for anything hand-numbered.
--
-- The batch number is searched too, so one box answers both "where is 114873"
-- and "show me BATCH-1001824".
SELECT b.* FROM batches b
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (b.created_at, b.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
AND (
    sqlc.narg('search')::text IS NULL
    OR b.batch_number ILIKE '%' || sqlc.narg('search')::text || '%'
    OR EXISTS (
        SELECT 1 FROM production_jobs j
        LEFT JOIN orders o ON o.id = j.order_id
        WHERE j.batch_id = b.id
          AND (
            j.job_number ILIKE '%' || sqlc.narg('search')::text || '%'
            OR o.order_number ILIKE '%' || sqlc.narg('search')::text || '%'
            OR j.personalisation_name ILIKE '%' || sqlc.narg('search')::text || '%'
          )
    )
)
ORDER BY b.created_at DESC, b.id DESC
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

-- name: SetBatchPrintError :exec
-- Records why a batch could not be sent to the printer. Called on every failure
-- path in printBatch so a Locked batch that failed is distinguishable from one
-- nobody has sent yet.
UPDATE batches SET
    print_error    = sqlc.arg('print_error'),
    print_error_at = now(),
    updated_at     = now()
WHERE id = sqlc.arg('id');

-- name: ClearBatchPrintError :exec
-- Clears the failure and records what the batch is now waiting on.
--
-- Both happen together because a successful send is exactly the moment the
-- previous failure stops being true. Two identifiers, because they arrive at
-- different times: the pipeline run is known as soon as BambuBuddy accepts the
-- work, while the queue item only exists once slicing has finished. The run id
-- is what stops the dispatcher sending the same plate twice in the meantime.
UPDATE batches SET
    print_error     = NULL,
    print_error_at  = NULL,
    queue_item_id   = sqlc.narg('queue_item_id'),
    pipeline_run_id = sqlc.narg('pipeline_run_id'),
    updated_at      = now()
WHERE id = sqlc.arg('id');

-- name: ListJobNumbersForBatches :many
-- Each batch's jobs, by job number.
--
-- For the Batches table's Jobs column, which shows WHICH orders are on a bed
-- rather than how many jobs it holds - "114556 114557 114558" answers the
-- question an operator actually has, where "4" does not. The order number is
-- read back out of the job number (JOB-114556), the same way the merged plate is
-- named, so the column and the file the operator downloads always agree.
--
-- Only the columns the decorated columns need: this is asked for a whole page
-- of batches at once, and they have no use for a job's full row. priority comes
-- along because the Batches table's Priority tab is answered per bed - a bed is
-- priority when any plank on it is - and asking per batch would be one query
-- per row on the page.
SELECT j.batch_id, j.job_number, j.colour, j.priority
FROM production_jobs j
WHERE j.batch_id = ANY(sqlc.arg('batch_ids')::uuid[])
ORDER BY j.job_number;

-- name: ListBatchesToDispatch :many
-- Batches that still have somewhere to go, OLDEST ORDER FIRST.
--
-- The opposite order to ListBatches, and deliberately: that one feeds a screen,
-- where newest-first is what a person expects, while this one feeds the
-- dispatcher, where the whole point is that the customer who has waited longest
-- gets on a printer first. batch_number comes from a sequence and the planner
-- builds beds oldest-order-first, so ascending batch number IS oldest order.
--
-- Both pre-print states are returned and the caller decides what each needs:
-- pending_approval wants approving, open wants sending once its plate has been
-- sliced. Anything further along (in_progress, completed) has left the queue.
--
-- Priority beds go ahead of that, which is the ONLY thing that overrides
-- longest-waiting. Forming beds priority-first is not enough on its own: a bed
-- carrying an expedited plank still reaches a printer in batch-number order,
-- so without this the customer who paid to jump the queue waits behind every
-- bed formed before theirs. min(priority) ASC NULLS LAST is the same
-- convention as the machine scheduler - LOWER IS MORE URGENT - and a bed with
-- no jobs sorts last rather than first.
--
-- A bed whose print already resolved is excluded. A failed plate keeps its
-- 'open' status - a failure is "still locked, and here is why" rather than a
-- lifecycle state - so without this the dispatcher would send it again on the
-- very next pass, into whatever went wrong the first time. Clearing the outcome
-- is the operator's deliberate act, through the Queue button.
SELECT b.* FROM batches b
WHERE b.status IN ('pending_approval', 'open')
  AND b.print_outcome IS NULL
ORDER BY (SELECT min(j.priority) FROM production_jobs j WHERE j.batch_id = b.id) ASC NULLS LAST,
         NULLIF(regexp_replace(b.batch_number, '\D', '', 'g'), '')::bigint ASC NULLS LAST,
         b.created_at ASC, b.id ASC;

-- name: ListLockedBedsWithRoom :many
-- Locked beds that still have a free place, best candidate for a top-up first.
--
-- A locked bed is invisible to the planner - ListReplannableJobs returns only
-- unbatched jobs and Draft members - so nothing has ever been able to fill the
-- gap left when a bed locks under-full or loses a plank. This is what lets an
-- expedited order take that place instead of waiting for a whole new bed.
--
-- The ordering is the interesting part.
--
-- NOT oldest-first, which is the obvious guess. ListBatchesToDispatch already
-- sorts min(priority) ASC ahead of batch number, so the moment a priority plank
-- joins ANY bed that bed jumps to the head of the dispatch queue - the bed's
-- age does not change when it prints, so it cannot discriminate.
--
-- What does discriminate is what the edit costs. A bed with no queue_item_id
-- was never handed to BambuBuddy: topping it up costs one re-plate. A bed
-- already sent costs a withdrawal, a re-upload and re-entry at the BACK of the
-- printer's queue, which can leave the expedited plank slower than if we had
-- left that bed alone. So unsent beds first (false sorts before true).
--
-- Fullest first within that: it disturbs the fewest committed plates for a
-- given amount of work, and it drives beds to the cap where they stop being
-- candidates at all. The usual "leave room on the emptier bed" argument does
-- not apply - a locked bed is out of the replanning pool, so its free places
-- are dead space nothing else will ever fill.
SELECT b.*, coalesce(u.units, 0)::int AS units_on_bed
FROM batches b
JOIN LATERAL (
    SELECT sum(j.quantity)::int AS units FROM production_jobs j WHERE j.batch_id = b.id
) u ON true
WHERE b.status = 'open'
  AND coalesce(u.units, 0) > 0
  AND coalesce(u.units, 0) < sqlc.arg('max_units')::int
ORDER BY (b.queue_item_id IS NOT NULL),
         coalesce(u.units, 0) DESC,
         NULLIF(regexp_replace(b.batch_number, '\D', '', 'g'), '')::bigint ASC NULLS LAST,
         b.created_at ASC, b.id ASC;

-- name: LockBatchRow :one
-- Takes a row lock on one bed, so two callers cannot both read "one place
-- free" and both fill it. Held for microseconds inside a transaction that does
-- no I/O.
SELECT id FROM batches WHERE id = sqlc.arg('id') FOR UPDATE;

-- name: ReopenBatchForReplanning :one
-- Returns a locked bed to being a Draft so the planner can refill it.
--
-- Used when a bed printed only partly: the planks that came off the printer are
-- completed and removed, and what is left is a bed with free places that nothing
-- would ever fill, because a locked bed has left the replanning pool.
--
-- Everything approval produced is cleared, not just the status. The merged plate
-- and its slice describe a bed that no longer exists, and queue_item_id /
-- pipeline_run_id say "already sent to BambuBuddy" - which the dispatcher reads
-- as "nothing to do", so a refilled bed carrying a stale queue id would sit at
-- four planks and never reach a printer.
--
-- Guarded on 'open': a bed that is printing or printed is a record of what
-- physically happened, and must not be reopened underneath it.
UPDATE batches SET
    status                   = 'pending_approval',
    approved_by              = NULL,
    approved_at              = NULL,
    merged_file_id           = NULL,
    plate_sliced_at          = NULL,
    plate_slice_error        = NULL,
    queue_item_id            = NULL,
    pipeline_run_id          = NULL,
    print_error              = NULL,
    print_error_at           = NULL,
    -- And what the last print did. A bed re-plated from different jobs that
    -- kept its archive_id would be completed by the next reconciliation pass
    -- against a print of planks it no longer holds.
    archive_id               = NULL,
    print_outcome            = NULL,
    print_started_at         = NULL,
    print_finished_at        = NULL,
    actual_print_time_minutes = NULL,
    actual_filament_grams    = NULL,
    filament_reserved        = false,
    updated_at               = now()
WHERE id = sqlc.arg('id') AND status = 'open'
RETURNING *;

-- name: ClearBatchPlateForEdit :one
-- Drops everything that described a bed's PREVIOUS contents, without changing
-- its status.
--
-- Used when a locked bed is edited by hand. The merged plate, its slice and its
-- BambuBuddy queue id all describe four planks that are no longer the four on
-- the bed; leaving any of them behind sends the old plate to a printer. The
-- queue id in particular reads to the dispatcher as "already sent", so the bed
-- would sit corrected in Tensor and never reach a machine.
--
-- Distinct from ReopenBatchForReplanning, which does the same clearing but also
-- unlocks the bed. A hand-edited bed stays exactly as locked as it was: the
-- operator changed its contents deliberately and does not want the planner
-- rearranging it afterwards.
UPDATE batches SET
    merged_file_id    = NULL,
    preview_file_id   = NULL,
    plate_sliced_at   = NULL,
    plate_slice_error = NULL,
    queue_item_id     = NULL,
    pipeline_run_id   = NULL,
    print_error       = NULL,
    print_error_at    = NULL,
    updated_at        = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListBatchesAwaitingPlateMeasurement :many
-- Beds that have been handed to BambuBuddy but carry no measured print time.
--
-- Every scheduling decision downstream is arithmetic on print time - when a
-- machine frees, which machine to build the next bed for - and until this is
-- filled in, that arithmetic is running on a MAX-of-jobs approximation, or on
-- nothing at all. Tensor used to measure it by slicing the plate itself; it does
-- not slice any more, BambuBuddy does, and BambuBuddy's queue item carries the
-- answer.
--
-- Ordered oldest first so a backlog is worked through in the order it was
-- queued, and capped by the caller rather than here.
SELECT id, batch_number, queue_item_id, units_per_bed
FROM batches
WHERE queue_item_id IS NOT NULL
  AND plate_sliced_at IS NULL
  AND status IN ('open', 'in_progress')
ORDER BY created_at ASC, id ASC;

-- name: ListBatchesInFlight :many
-- Beds that have been handed to BambuBuddy and whose print has not resolved.
--
-- The reconciliation pass's working set. plate_filename comes from the file
-- actually uploaded (SendBatchToPrinter sends merged_file_id, falling back to
-- preview_file_id), because the filename is the last-resort correlation key when
-- BambuBuddy's queue no longer holds the item - and it must be the SAME name
-- that was sent, not one rebuilt from the batch.
--
-- print_outcome IS NULL is what keeps a resolved bed out of the set for ever.
SELECT b.*, f.filename AS plate_filename
FROM batches b
LEFT JOIN file_assets f ON f.id = COALESCE(b.merged_file_id, b.preview_file_id)
WHERE b.status IN ('open', 'in_progress')
  AND (b.queue_item_id IS NOT NULL OR b.pipeline_run_id IS NOT NULL)
  AND b.print_outcome IS NULL
ORDER BY b.approved_at ASC NULLS LAST, b.batch_number ASC;

-- name: SetBatchQueueItem :exec
-- Fills in the queue item a bed was sliced into.
--
-- Slicing is asynchronous, so RunPipeline answers 202 with no queue entry yet
-- and recordQueued leaves this null. Nothing ever filled it in, which also meant
-- ListBatchesAwaitingPlateMeasurement - which requires it - never saw those
-- beds and they were never measured.
--
-- Guarded on the column still being null so a backfill can never overwrite the
-- id a later send established.
UPDATE batches SET queue_item_id = sqlc.arg('queue_item_id'), updated_at = now()
WHERE id = sqlc.arg('id') AND queue_item_id IS NULL;

-- name: RecordBatchPrintOutcome :one
-- Claims a finished print, exactly once.
--
-- The idempotency gate for the whole completion path: one statement, guarded on
-- print_outcome still being null. The reconciliation pass runs every fleet sync
-- and a push event can arrive twice, so several callers may race here - exactly
-- one gets a row back and the rest correctly do nothing. No rows returned means
-- somebody else already recorded it.
--
-- Deliberately written BEFORE the status change, not after: a crash between the
-- two leaves a bed with an outcome and no completion, which the next pass can
-- see and repair. The other order leaves a completed bed with no record of why.
UPDATE batches SET
    print_outcome             = sqlc.arg('print_outcome'),
    archive_id                = sqlc.narg('archive_id'),
    print_started_at          = COALESCE(sqlc.narg('print_started_at'), print_started_at),
    print_finished_at         = sqlc.narg('print_finished_at'),
    actual_print_time_minutes = sqlc.narg('actual_print_time_minutes'),
    actual_filament_grams     = sqlc.narg('actual_filament_grams'),
    updated_at                = now()
WHERE id = sqlc.arg('id') AND print_outcome IS NULL
RETURNING *;

-- name: ClearBatchPrintOutcome :exec
-- Forgets a failed print so the bed can be sent again.
--
-- Only ever from the manual Queue button: the deliberate human "yes, run that
-- again". ListBatchesToDispatch excludes a bed with an outcome, so without this
-- a failed bed would be retried automatically for ever against whatever went
-- wrong the first time.
--
-- The send identifiers go with it. SendBatchToPrinter refuses a bed that
-- already carries a queue item or a pipeline run - the guard against printing
-- one bed twice - so leaving them behind would make a failed bed permanently
-- un-resendable, and the only escape would be editing it.
UPDATE batches SET
    print_outcome             = NULL,
    archive_id                = NULL,
    print_finished_at         = NULL,
    print_error               = NULL,
    print_error_at            = NULL,
    queue_item_id             = NULL,
    pipeline_run_id           = NULL,
    updated_at                = now()
WHERE id = sqlc.arg('id');

-- name: ListBatchesResolvedButNotClosed :many
-- Beds whose print resolved as finished but which never reached 'completed'.
--
-- The repair set for a crash between RecordBatchPrintOutcome and the status
-- change. Without this a bed would sit for ever holding an outcome nobody acted
-- on, and its planks would never reach Assembly.
SELECT * FROM batches
WHERE print_outcome = 'completed' AND status <> 'completed'
ORDER BY print_finished_at ASC NULLS LAST;
