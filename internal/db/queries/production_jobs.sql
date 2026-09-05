-- Production jobs: the print-queue core. filament_grams_required cast to float8;
-- nullable columns stay pointers. The lifecycle mutations are explicit queries so
-- the business rules that gate them live in the service, not in SQL.

-- name: InsertProductionJob :one
INSERT INTO production_jobs (
    id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
    qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
    nozzle_profile, filament_grams_required, print_file_id, estimated_print_time_minutes,
    due_date, priority, personalisation_name, personalisation_font, personalisation_colour,
    personalisation_variant, personalisation_status, name_confirmed, photo_confirmed,
    font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
    personalisation_notes, personalisation_photo_file_id, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
    colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
    quality_mm, machine_family, variant_title, personalisation_properties,
    model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
    support_weight_g, purge_weight_g, colour_count
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_number'), sqlc.narg('order_id'), sqlc.narg('batch_id'),
    sqlc.arg('description'), sqlc.arg('quantity'), sqlc.arg('status'), sqlc.arg('assembly_status'),
    sqlc.arg('qc_status'), sqlc.arg('packaging_status'), sqlc.narg('shopify_order_id'),
    sqlc.narg('sku'), sqlc.narg('product_name'), sqlc.narg('material'), sqlc.narg('colour'),
    sqlc.narg('nozzle_profile'), sqlc.narg('filament_grams_required')::float8, sqlc.narg('print_file_id'),
    sqlc.narg('estimated_print_time_minutes'), sqlc.narg('due_date'), sqlc.arg('priority'),
    sqlc.narg('personalisation_name'), sqlc.narg('personalisation_font'), sqlc.narg('personalisation_colour'),
    sqlc.narg('personalisation_variant'), sqlc.arg('personalisation_status'), sqlc.arg('name_confirmed'),
    sqlc.arg('photo_confirmed'), sqlc.arg('font_confirmed'), sqlc.arg('colour_confirmed'),
    sqlc.arg('variant_confirmed'), sqlc.arg('customer_approval_received'), sqlc.narg('personalisation_notes'),
    sqlc.narg('personalisation_photo_file_id'), sqlc.narg('reprint_of_job_id'), sqlc.narg('split_of_job_id'),
    sqlc.narg('shopify_customer_id'), sqlc.narg('customer_name'), sqlc.arg('held'),
    sqlc.arg('colours'), sqlc.narg('support_used'), sqlc.narg('infill_pct')::float8,
    sqlc.narg('left_nozzle_mm')::float8, sqlc.narg('right_nozzle_mm')::float8,
    sqlc.narg('flow_pct')::float8, sqlc.narg('quality_mm')::float8, sqlc.narg('machine_family'),
    sqlc.narg('variant_title'),
    -- COALESCE, not a bare argument: the column is NOT NULL with a '[]'
    -- default, but naming it in the INSERT means the default never applies -
    -- so any caller that does not set it would fail the constraint. Every
    -- job has a line to snapshot; not every caller has one to hand.
    COALESCE(sqlc.narg('personalisation_properties')::jsonb, '[]'::jsonb),
    sqlc.narg('model_error'), sqlc.narg('model_error_at'),
    sqlc.narg('issue_reason'), sqlc.narg('bbox_x_mm')::float8, sqlc.narg('bbox_y_mm')::float8,
    sqlc.narg('bbox_z_mm')::float8, sqlc.narg('support_weight_g')::float8,
    sqlc.narg('purge_weight_g')::float8, sqlc.narg('colour_count')
)
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: GetProductionJobByID :one
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at
FROM production_jobs WHERE id = $1;

-- name: GetProductionJobByNumber :one
-- The same row, found by the number a person actually reads and quotes.
--
-- Job numbers are unique (uq_production_jobs_job_number) and derived from the
-- Shopify order, so "JOB-114556" identifies a job as precisely as its uuid
-- does - and is the only one of the two anybody can say out loud.
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at
FROM production_jobs WHERE job_number = sqlc.arg('job_number');

-- name: ListProductionJobs :many
-- Full list, newest first, with optional status / assembly_status / finishing_status / qc_status /
-- packaging_status filters (null = any).
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('assembly_status')::text IS NULL OR assembly_status = sqlc.narg('assembly_status')::text)
  AND (sqlc.narg('finishing_status')::text IS NULL OR finishing_status = sqlc.narg('finishing_status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
  AND (sqlc.narg('packaging_status')::text IS NULL OR packaging_status = sqlc.narg('packaging_status')::text)
  AND (sqlc.narg('order_id')::uuid IS NULL OR order_id = sqlc.narg('order_id')::uuid)
  AND (sqlc.narg('batch_id')::uuid IS NULL OR batch_id = sqlc.narg('batch_id')::uuid)
-- Newest ORDER first, matching the Orders page, not newest job.
--
-- created_at is when Tensor happened to build the row, which on a bulk import
-- is the same second for dozens of them - so it sorted by nothing and the list
-- read as shuffled. What an operator means by "latest" is the customer's own
-- order date, and it is the one thing the two pages must agree on: a job and
-- its order have to appear in the same relative position or the lists cannot
-- be read side by side.
--
-- A subquery rather than a join: the column list above is the whole
-- production_jobs row, and joining would change the result shape into a Row
-- type instead of the shared ProductionJob one.
ORDER BY COALESCE(
    (SELECT o.placed_at FROM orders o WHERE o.id = production_jobs.order_id),
    created_at
) DESC, created_at DESC, id DESC;

-- name: ListProductionJobsPage :many
-- Keyset page over (created_at, id) with the same optional filters.
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('assembly_status')::text IS NULL OR assembly_status = sqlc.narg('assembly_status')::text)
  AND (sqlc.narg('finishing_status')::text IS NULL OR finishing_status = sqlc.narg('finishing_status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
  AND (sqlc.narg('packaging_status')::text IS NULL OR packaging_status = sqlc.narg('packaging_status')::text)
  AND (sqlc.narg('order_id')::uuid IS NULL OR order_id = sqlc.narg('order_id')::uuid)
  AND (sqlc.narg('batch_id')::uuid IS NULL OR batch_id = sqlc.narg('batch_id')::uuid)
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: CountJobsForOrder :one
SELECT count(*) FROM production_jobs WHERE order_id = $1;

-- name: UpdateProductionJobFields :one
-- Applies the role-gated PATCH fields. Each null arg leaves its column unchanged;
-- batch_id and issue_reason use a set-flag so they can be cleared to NULL
-- explicitly - COALESCE cannot express "set this to NULL", only "leave it".
UPDATE production_jobs SET
    status           = COALESCE(sqlc.narg('status'), status),
    assembly_status  = COALESCE(sqlc.narg('assembly_status'), assembly_status),
    finishing_status = COALESCE(sqlc.narg('finishing_status'), finishing_status),
    qc_status        = COALESCE(sqlc.narg('qc_status'), qc_status),
    packaging_status = COALESCE(sqlc.narg('packaging_status'), packaging_status),
    priority         = COALESCE(sqlc.narg('priority'), priority),
    held             = COALESCE(sqlc.narg('held'), held),
    batch_id         = CASE WHEN sqlc.arg('set_batch_id')::bool THEN sqlc.narg('batch_id') ELSE batch_id END,
    issue_reason     = CASE WHEN sqlc.arg('set_issue_reason')::bool THEN sqlc.narg('issue_reason') ELSE issue_reason END,
    updated_at       = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: ValidateProductionJobPersonalisation :one
UPDATE production_jobs SET
    name_confirmed                = sqlc.arg('name_confirmed'),
    photo_confirmed               = sqlc.arg('photo_confirmed'),
    font_confirmed                = sqlc.arg('font_confirmed'),
    colour_confirmed              = sqlc.arg('colour_confirmed'),
    variant_confirmed             = sqlc.arg('variant_confirmed'),
    customer_approval_received    = sqlc.arg('customer_approval_received'),
    personalisation_notes         = sqlc.narg('personalisation_notes'),
    personalisation_photo_file_id = sqlc.narg('personalisation_photo_file_id'),
    personalisation_status        = sqlc.arg('personalisation_status'),
    personalisation_validated_by  = sqlc.narg('personalisation_validated_by'),
    personalisation_validated_at  = sqlc.narg('personalisation_validated_at'),
    updated_at                    = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: SetProductionJobPrintFile :one
-- Clears issue_reason only when it is exactly 'stl_missing' - the flag this
-- upload is the direct remedy for (see applyMatch). Leaving it set would keep
-- the job out of ListBatchableJobs after an operator did the one thing that
-- fixes it. Any other reason survives untouched: an STL upload does not fix
-- colour_missing or filament_out_of_stock, and blanket-clearing would push a
-- genuinely unvalidated job into batching.
UPDATE production_jobs SET
    print_file_id = sqlc.arg('print_file_id'),
    issue_reason  = CASE WHEN issue_reason = 'stl_missing' THEN NULL ELSE issue_reason END,
    updated_at    = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: SetProductionJobStatus :one
UPDATE production_jobs SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: ListBatchableJobs :many
-- Unbatched, queued jobs whose personalisation is resolved and which cleared
-- Stage 3 validation (no issue_reason), oldest first (FCFS). A flagged job is
-- never batched until whatever is wrong (missing SKU, STL, colour, ...) is fixed.
-- quantity > 0 excludes a split job's original row once every unit of it has
-- been peeled off into split_of_job_id rows across earlier batches - it stays
-- around at quantity 0 purely as the split group's root for progress
-- tracking (see GetSplitJobProgress), never as something left to batch.
--
-- held = false belongs here for the same reason issue_reason does. A hold is an
-- operator saying "not this one, not yet", and it used to be enforced only at
-- ApproveBatchFor - by which point the held job had already been planned onto a
-- bed, so its hold blocked approval of every unrelated job planned beside it,
-- and the planner rebuilt that same doomed bed every cycle. Keeping it out of
-- the pool means the rest of the bed batches normally. The approval check stays
-- as defence in depth, for a job held after its batch was planned.
SELECT * FROM production_jobs
WHERE batch_id IS NULL
  AND status = 'queued'
  AND quantity > 0
  AND personalisation_status IN ('validated', 'not_required')
  AND issue_reason IS NULL
  AND held = false
ORDER BY created_at ASC, id ASC;

-- name: CountBatchableJobs :one
-- Cheap count-only companion to ListBatchableJobs (identical WHERE clause,
-- kept in sync deliberately) for the trigger-threshold check - is it worth
-- enqueuing a replan yet? - without paying for the full row scan/marshal
-- ListBatchableJobs does.
SELECT count(*) FROM production_jobs
WHERE batch_id IS NULL
  AND status = 'queued'
  AND quantity > 0
  AND personalisation_status IN ('validated', 'not_required')
  AND issue_reason IS NULL
  AND held = false;

-- name: ListJobsForBatch :many
SELECT * FROM production_jobs WHERE batch_id = $1 ORDER BY created_at ASC, id ASC;

-- name: ListUnassignedCompatibleJobs :many
-- Unassigned jobs sharing a reference job's physical/slicing-profile
-- signature (material, both nozzle diameters, quality, machine family) -
-- "same machine configuration" for manually adding a job to an existing
-- Draft batch (see production.CompatibilityKey). Eligibility bar mirrors
-- ListBatchableJobs (queued, no issue) plus the compatibility filter. IS NOT
-- DISTINCT FROM (not =) so a null-vs-null field (e.g. a single-nozzle job's
-- unused right nozzle) still counts as matching, same as the planner's own
-- grouping key treats it.
SELECT * FROM production_jobs
WHERE batch_id IS NULL
  AND status = 'queued'
  AND quantity > 0
  AND personalisation_status IN ('validated', 'not_required')
  AND issue_reason IS NULL
  AND held = false
  AND material IS NOT DISTINCT FROM sqlc.narg('material')::text
  AND left_nozzle_mm::float8 IS NOT DISTINCT FROM sqlc.narg('left_nozzle_mm')::float8
  AND right_nozzle_mm::float8 IS NOT DISTINCT FROM sqlc.narg('right_nozzle_mm')::float8
  AND quality_mm::float8 IS NOT DISTINCT FROM sqlc.narg('quality_mm')::float8
  AND machine_family IS NOT DISTINCT FROM sqlc.narg('machine_family')::text
ORDER BY created_at ASC, id ASC;

-- name: RemoveJobFromBatch :one
-- Detaches one job from its batch (Draft-only editing - enforced by the
-- caller checking the batch's status before calling this). Scoped to
-- batch_id = $2 as a safety check so a stale/wrong batch id in the request
-- can't silently detach a job from a different batch than the URL named.
-- Zero rows back (pgx.ErrNoRows) means the job isn't actually on that batch.
UPDATE production_jobs SET batch_id = NULL, updated_at = now()
WHERE id = sqlc.arg('id') AND batch_id = sqlc.arg('batch_id')
RETURNING *;

-- name: CountJobsInBatch :one
SELECT count(*) FROM production_jobs WHERE batch_id = $1;

-- name: AssignJobsToBatch :exec
-- Puts jobs on a batch. A job may only be moved if it is unassigned or still
-- sitting in a Draft; one that belongs to an approved, printing or completed
-- batch is left exactly where it is.
--
-- That guard is not theoretical. The planner reads its pool, plans, and only
-- then writes - and a human can approve a Draft in that window. The dissolve
-- step correctly refuses to touch the newly-approved batch, but without this
-- the plan built moments earlier would still reassign its jobs onto a fresh
-- Draft, stealing the contents of a plate whose filament is already reserved
-- and which a machine is about to print.
UPDATE production_jobs SET batch_id = sqlc.arg('batch_id'), updated_at = now()
WHERE id = ANY(sqlc.arg('job_ids')::uuid[])
  AND (batch_id IS NULL
       OR batch_id IN (SELECT id FROM batches WHERE status = 'pending_approval'));

-- name: CompleteProductionJobsForBatch :execrows
-- Auto-completes every non-terminal job on a batch that just finished
-- printing. A job already 'failed' is excluded on purpose: the bed as a
-- whole finished, but this job's own print did not succeed (its reprint
-- was already queued via /fail) - force-completing it would erase that
-- failure and let a bad print slip into assembly/QC as if it passed.
UPDATE production_jobs
SET status = 'completed', updated_at = now()
WHERE batch_id = $1 AND status NOT IN ('completed', 'failed');

-- name: DecrementProductionJobQuantity :one
-- Shrinks a job's remaining quantity by delta after some of it was peeled
-- off into a new split_of_job_id row for a batch that's actually being
-- created this run (see AutoCreateBatches) - the row keeps representing
-- whatever's left, still queued and batchable next run if delta didn't
-- consume all of it.
UPDATE production_jobs SET quantity = quantity - sqlc.arg('delta'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          finishing_status, qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, split_of_job_id, shopify_customer_id, customer_name, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, variant_title, personalisation_properties,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at;

-- name: GetSplitJobProgress :one
-- Total ordered vs completed quantity across a split job's whole group (the
-- root row plus every split_of_job_id fragment peeled off it) - "150 total /
-- 72 printed / 78 remaining" derived on read, nothing stored. Safe to call
-- with any job id in the group, root or fragment - root_id resolves to the
-- job's own id when it was never split (every job is its own group of one).
WITH target AS (
    SELECT COALESCE(t.split_of_job_id, t.id) AS root_id
    FROM production_jobs t
    WHERE t.id = sqlc.arg('id')
)
SELECT
    target.root_id,
    COALESCE(sum(pj.quantity), 0)::bigint AS total_quantity,
    COALESCE(sum(pj.quantity) FILTER (WHERE pj.status = 'completed'), 0)::bigint AS completed_quantity
FROM target
JOIN production_jobs pj ON COALESCE(pj.split_of_job_id, pj.id) = target.root_id
GROUP BY target.root_id;

-- name: InsertProductionJobFailure :one
INSERT INTO production_job_failures (
    id, job_id, stage, reason, notes, filament_wasted_grams, time_wasted_minutes, created_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('stage'), sqlc.arg('reason'), sqlc.narg('notes'),
    sqlc.narg('filament_wasted_grams')::float8, sqlc.narg('time_wasted_minutes'), sqlc.arg('created_by')
)
RETURNING id, job_id, stage, reason, notes,
          filament_wasted_grams, time_wasted_minutes,
          created_by, created_at;

-- name: CountJobsNotPackagedForOrder :one
-- Order-level dispatch readiness: how many of an order's products are not yet
-- packed. A failed job is excluded - its reprint is the row that still has to
-- get there, and counting both would make the order permanently unready.
SELECT count(*) FROM production_jobs
WHERE order_id = sqlc.arg('order_id')
  AND status <> 'failed'
  AND packaging_status <> 'packaged';

-- name: CountJobsForOrderTotal :one
SELECT count(*) FROM production_jobs
WHERE order_id = sqlc.arg('order_id') AND status <> 'failed';

-- name: LockOrderForJobCreation :exec
-- Serialises job creation for one order across processes, for the rest of the
-- caller's transaction.
--
-- Necessary because CountJobsForOrder cannot see a concurrent transaction's
-- uncommitted inserts under READ COMMITTED (which is what InTx/InTxWith use):
-- two create_jobs_from_order runs for the same order would both count zero and
-- both insert. That is not hypothetical - importShopifyOrder enqueues a job on
-- every re-sync of the same order, and the manual backfill endpoint can race
-- either of them.
--
-- A unique constraint cannot do this job instead: an order may legitimately
-- contain the same SKU twice with different personalisation, so (order_id, sku)
-- is not unique and there is no natural key without inventing a line-item index.
--
-- Keyed on the order id alone, so different orders never block each other. The
-- second argument namespaces the key against any future advisory-lock user.
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('order_id')::text, 8021));

-- name: NextJobNumber :one
-- Mints the next job number from a sequence. Replaces production.NewJobNumber's
-- 5 random digits, which had a 100k space, no unique index, and therefore a
-- collision probability that grew with the table - a pure function pretending
-- to a uniqueness guarantee it structurally could not provide. Sequence gaps on
-- a rolled-back transaction are harmless.
SELECT ('JOB-' || nextval('production_job_number_seq')::text)::text AS job_number;

-- name: ListReplannableJobs :many
-- Everything the batch planner may reconsider on a run: jobs not yet on a bed,
-- PLUS jobs currently sitting in a Draft (pending_approval) batch.
--
-- The second half is what makes batching a loop instead of a one-shot. With
-- only `batch_id IS NULL` - which is what ListBatchableJobs gives, and all the
-- planner ever saw - a job was frozen into whatever bed it first landed on, so
-- a Draft that formed at 40% utilisation stayed at 40% forever no matter how
-- much compatible work arrived afterwards.
--
-- A Draft is a proposal, not a commitment: no filament is reserved (that
-- happens at approval) and no plate is promised, so its jobs are free to be
-- re-planned. Approved batches and beyond are deliberately absent - their
-- jobs have left 'queued' anyway, so the status filter excludes them twice
-- over.
--
-- Ordered by when the CUSTOMER placed the order, not when the job row was
-- written. The colour strategy serves this list in order (see
-- production.GroupByColour), so "oldest order first" is exactly this ORDER BY -
-- and j.created_at cannot express it: jobs created by one import share a
-- timestamp to the microsecond, so ordering by it served a batch of 90 orders in
-- essentially arbitrary sequence. placed_at falls back to created_at for a job
-- with no linked order (a reprint, a manually-added job), which keeps those in
-- their own arrival order rather than sorting them all to the front.
SELECT j.* FROM production_jobs j
LEFT JOIN batches b ON b.id = j.batch_id
LEFT JOIN orders o ON o.id = j.order_id
WHERE (j.batch_id IS NULL OR b.status = 'pending_approval')
  AND j.status = 'queued'
  AND j.quantity > 0
  AND j.personalisation_status IN ('validated', 'not_required')
  AND j.issue_reason IS NULL
  AND j.held = false
ORDER BY COALESCE(o.placed_at, j.created_at) ASC, j.job_number ASC, j.id ASC;

-- name: UnassignJobsFromBatches :exec
-- Detaches jobs from the given batches so they can be re-planned. Guarded on
-- the batch still being a Draft, so a batch approved between the planner
-- reading the pool and writing its result keeps every one of its jobs.
UPDATE production_jobs SET batch_id = NULL, updated_at = now()
WHERE batch_id = ANY(sqlc.arg('batch_ids')::uuid[])
  AND batch_id IN (SELECT id FROM batches WHERE status = 'pending_approval');

-- name: ListExcludedQueuedJobs :many
-- Queued, unbatched jobs the planner will NOT see, and the facts needed to say
-- why. The exact complement of ListBatchableJobs' eligibility bar, over the
-- same queued/quantity base.
--
-- Without this a held or unvalidated job simply is not in any planner output:
-- it is not batched, not held below target, not unbatchable, not deferred -
-- it is absent, and absent looks identical to lost. Reporting the exclusion is
-- what makes "why is this job not on a bed?" answerable for every job rather
-- than only the ones planning happened to reach.
SELECT id, job_number, held, personalisation_status, issue_reason
FROM production_jobs
WHERE batch_id IS NULL
  AND status = 'queued'
  AND quantity > 0
  AND (
    held = true
    OR personalisation_status NOT IN ('validated', 'not_required')
    OR issue_reason IS NOT NULL
  )
ORDER BY created_at ASC, id ASC;

-- name: SetProductionJobMachineFamily :one
-- Records which printer family a job must run on, for a job whose family does
-- not come from a matched design - a personalised model Tensor rendered itself.
--
-- Clears issue_reason only when it is exactly 'profile_missing', mirroring
-- SetProductionJobPrintFile's treatment of 'stl_missing': supplying the missing
-- fact resolves that one issue and must not paper over a different one.
UPDATE production_jobs SET
    machine_family = sqlc.arg('machine_family'),
    issue_reason   = CASE WHEN issue_reason = 'profile_missing' THEN NULL ELSE issue_reason END,
    updated_at     = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: JobNumberExists :one
-- Whether a job number is already taken.
--
-- Job numbers are derived from the Shopify order, which is unique, so this
-- should never find anything. It exists because the alternative failure is
-- bad out of proportion to the check: a duplicate violates the unique index,
-- which aborts the whole transaction, and an order silently ends up with no
-- jobs at all.
SELECT EXISTS (SELECT 1 FROM production_jobs WHERE job_number = sqlc.arg('job_number'));

-- name: SetJobModelError :exec
-- Records why a generated model could not be built, and when.
--
-- Deliberately does NOT touch issue_reason: the job already carries
-- 'stl_missing' (it has no print file), which is what keeps it out of
-- batching. This is the explanation, not the exclusion.
UPDATE production_jobs SET
    model_error    = sqlc.arg('model_error'),
    model_error_at = now(),
    updated_at     = now()
WHERE id = sqlc.arg('id');

-- name: ClearJobModelError :exec
-- Clears a previous render failure. Called when a model lands, whether the
-- retry succeeded or somebody uploaded one by hand - either way the job is no
-- longer waiting on a render that failed.
UPDATE production_jobs SET
    model_error    = NULL,
    model_error_at = NULL,
    updated_at     = now()
WHERE id = sqlc.arg('id');


-- name: CompleteJobsForFulfilledOrder :many
-- Marks an order's outstanding jobs done and takes them off any bed that has
-- not been committed to a machine.
--
-- Shopify saying an order is fulfilled is the end of the story: the parcel has
-- left, so nothing about it is still work for the floor. Left alone, its jobs sit
-- in the queue forever and occupy bed space that live orders need.
--
-- Only jobs still in flight are touched. A job that already failed keeps its
-- failure - its reprint is what is being fulfilled, and overwriting it would
-- erase why there was a reprint at all.
--
-- The batch link is cleared ONLY while the bed is still a proposal or merely
-- approved. A batch that is printing or printed is a record of what physically
-- happened, and quietly removing a job from it would make that record a lie -
-- so the job is completed but its membership stands.
UPDATE production_jobs j SET
    status     = 'completed',
    batch_id   = CASE
                   WHEN b.status IS NULL OR b.status IN ('pending_approval', 'open')
                     THEN NULL
                   ELSE j.batch_id
                 END,
    updated_at = now()
FROM (
    SELECT p.id, p.batch_id FROM production_jobs p WHERE p.order_id = sqlc.arg('order_id')
) src
LEFT JOIN batches b ON b.id = src.batch_id
WHERE j.id = src.id
  AND j.status IN ('queued', 'in_production')
RETURNING j.id, src.batch_id AS freed_batch_id;

-- name: CompleteSelectedJobsOnBatch :many
-- Marks the named jobs on a bed done, and only those.
--
-- A plate comes off the printer and an operator checks it plank by plank: three
-- are good, one warped. So finishing a bed is a selection, not a switch - this
-- takes the ids that were ticked and leaves the rest exactly where they are, on
-- the bed, still to be dealt with.
--
-- The jobs stay ON the bed rather than being detached, unlike the fulfilment
-- path: a bed that has printed is a record of what physically ran, and the
-- Completed list reads its orders and colours from the jobs pointing at it.
--
-- A job already 'failed' is left alone for the reason
-- CompleteProductionJobsForBatch gives: its reprint is already queued, and
-- force-completing it would erase that.
UPDATE production_jobs
SET status = 'completed', updated_at = now()
WHERE batch_id = sqlc.arg('batch_id')
  AND id = ANY(sqlc.arg('job_ids')::uuid[])
  AND status NOT IN ('completed', 'failed')
RETURNING id, job_number;

-- name: CountUnfinishedJobsOnBatch :one
-- How many planks on a bed are still to be dealt with.
--
-- Zero is what turns a bed Done. 'failed' counts as dealt with: that plank's
-- reprint is a job of its own on some future bed, so the bed it failed on is
-- finished with it.
SELECT count(*) FROM production_jobs
WHERE batch_id = $1 AND status NOT IN ('completed', 'failed');
