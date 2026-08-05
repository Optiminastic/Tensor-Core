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
    personalisation_notes, personalisation_photo_file_id, reprint_of_job_id, held,
    colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
    quality_mm, machine_family, issue_reason
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
    sqlc.narg('personalisation_photo_file_id'), sqlc.narg('reprint_of_job_id'), sqlc.arg('held'),
    sqlc.arg('colours'), sqlc.narg('support_used'), sqlc.narg('infill_pct')::float8,
    sqlc.narg('left_nozzle_mm')::float8, sqlc.narg('right_nozzle_mm')::float8,
    sqlc.narg('flow_pct')::float8, sqlc.narg('quality_mm')::float8, sqlc.narg('machine_family'),
    sqlc.narg('issue_reason')
)
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at;

-- name: GetProductionJobByID :one
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at
FROM production_jobs WHERE id = $1;

-- name: ListProductionJobs :many
-- Full list, newest first, with optional status / qc_status filters (null = any).
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
  AND (sqlc.narg('order_id')::uuid IS NULL OR order_id = sqlc.narg('order_id')::uuid)
  AND (sqlc.narg('batch_id')::uuid IS NULL OR batch_id = sqlc.narg('batch_id')::uuid)
ORDER BY created_at DESC, id DESC;

-- name: ListProductionJobsPage :many
-- Keyset page over (created_at, id) with the same optional filters.
SELECT id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
       qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
       nozzle_profile, filament_grams_required, print_file_id,
       estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
       personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
       photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
       personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
       personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
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
-- batch_id uses a set-flag so it can be cleared to NULL explicitly.
UPDATE production_jobs SET
    status           = COALESCE(sqlc.narg('status'), status),
    assembly_status  = COALESCE(sqlc.narg('assembly_status'), assembly_status),
    qc_status        = COALESCE(sqlc.narg('qc_status'), qc_status),
    packaging_status = COALESCE(sqlc.narg('packaging_status'), packaging_status),
    priority         = COALESCE(sqlc.narg('priority'), priority),
    held             = COALESCE(sqlc.narg('held'), held),
    batch_id         = CASE WHEN sqlc.arg('set_batch_id')::bool THEN sqlc.narg('batch_id') ELSE batch_id END,
    updated_at       = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at;

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
          qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at;

-- name: SetProductionJobPrintFile :one
UPDATE production_jobs SET print_file_id = sqlc.arg('print_file_id'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at;

-- name: SetProductionJobStatus :one
UPDATE production_jobs SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, job_number, order_id, batch_id, description, quantity, status, assembly_status,
          qc_status, packaging_status, shopify_order_id, sku, product_name, material, colour,
          nozzle_profile, filament_grams_required, print_file_id,
          estimated_print_time_minutes, due_date, priority, personalisation_name, personalisation_font,
          personalisation_colour, personalisation_variant, personalisation_status, name_confirmed,
          photo_confirmed, font_confirmed, colour_confirmed, variant_confirmed, customer_approval_received,
          personalisation_notes, personalisation_photo_file_id, personalisation_validated_by,
          personalisation_validated_at, reprint_of_job_id, held,
          colours, support_used, infill_pct, left_nozzle_mm, right_nozzle_mm, flow_pct,
          quality_mm, machine_family, issue_reason, created_at, updated_at;

-- name: ListBatchableJobs :many
-- Unbatched, queued jobs whose personalisation is resolved and which cleared
-- Stage 3 validation (no issue_reason), oldest first (FCFS). A flagged job is
-- never batched until whatever is wrong (missing SKU, STL, colour, ...) is fixed.
SELECT * FROM production_jobs
WHERE batch_id IS NULL
  AND status = 'queued'
  AND personalisation_status IN ('validated', 'not_required')
  AND issue_reason IS NULL
ORDER BY created_at ASC, id ASC;

-- name: ListJobsForBatch :many
SELECT * FROM production_jobs WHERE batch_id = $1 ORDER BY created_at ASC, id ASC;

-- name: CountJobsInBatch :one
SELECT count(*) FROM production_jobs WHERE batch_id = $1;

-- name: AssignJobsToBatch :exec
UPDATE production_jobs SET batch_id = sqlc.arg('batch_id'), updated_at = now()
WHERE id = ANY(sqlc.arg('job_ids')::uuid[]);

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
