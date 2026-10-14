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
    support_weight_g, purge_weight_g, colour_count, part_role
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
    sqlc.narg('purge_weight_g')::float8, sqlc.narg('colour_count'),
    -- Defaulted in the column, but named here so a caller that knows which
    -- part it is building can say so. 'body' where nobody does.
    coalesce(sqlc.narg('part_role')::text, 'body')
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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at
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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at
FROM production_jobs WHERE job_number = sqlc.arg('job_number');

-- name: ListProductionJobs :many
-- personalisation_properties is NOT sent by this list, and that is the single
-- biggest thing about its cost: it is 139 kB of the table's 304 kB, 46% of every
-- byte this endpoint moves, and nothing on the list screen reads it. Only the
-- job DETAIL page does, through GetProductionJobByID, which still carries it.
--
-- Selected as a constant rather than dropped, because the projection has to
-- stay column-for-column identical to the table or sqlc stops mapping it to
-- gen.ProductionJob and every caller changes shape.
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
          quality_mm, machine_family, variant_title,
          '[]'::jsonb AS personalisation_properties,
          -- Emptied, not dropped, for the same reason as the line above -
          -- but NULL is kept as NULL, because null here means "the customer
          -- changed nothing" and that one bit is what the list's
          -- personalisation tag reads. The values themselves are heavy and
          -- only the detail page needs them.
          CASE WHEN personalisation_original IS NULL THEN NULL
               ELSE '{}'::jsonb END AS personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('assembly_status')::text IS NULL OR assembly_status = sqlc.narg('assembly_status')::text)
  AND (sqlc.narg('finishing_status')::text IS NULL OR finishing_status = sqlc.narg('finishing_status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
  AND (sqlc.narg('packaging_status')::text IS NULL OR packaging_status = sqlc.narg('packaging_status')::text)
  AND (sqlc.narg('order_id')::uuid IS NULL OR order_id = sqlc.narg('order_id')::uuid)
  AND (sqlc.narg('batch_id')::uuid IS NULL OR batch_id = sqlc.narg('batch_id')::uuid)
  -- Same box as ListProductionJobsPage; see there for why these four fields.
  AND (
    sqlc.narg('search')::text IS NULL
    OR job_number ILIKE '%' || sqlc.narg('search')::text || '%'
    OR personalisation_name ILIKE '%' || sqlc.narg('search')::text || '%'
    OR customer_name ILIKE '%' || sqlc.narg('search')::text || '%'
    OR product_name ILIKE '%' || sqlc.narg('search')::text || '%'
  )
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
-- personalisation_properties is NOT sent by this list, and that is the single
-- biggest thing about its cost: it is 139 kB of the table's 304 kB, 46% of every
-- byte this endpoint moves, and nothing on the list screen reads it. Only the
-- job DETAIL page does, through GetProductionJobByID, which still carries it.
--
-- Selected as a constant rather than dropped, because the projection has to
-- stay column-for-column identical to the table or sqlc stops mapping it to
-- gen.ProductionJob and every caller changes shape.
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
          quality_mm, machine_family, variant_title,
          '[]'::jsonb AS personalisation_properties,
          -- Emptied, not dropped, for the same reason as the line above -
          -- but NULL is kept as NULL, because null here means "the customer
          -- changed nothing" and that one bit is what the list's
          -- personalisation tag reads. The values themselves are heavy and
          -- only the detail page needs them.
          CASE WHEN personalisation_original IS NULL THEN NULL
               ELSE '{}'::jsonb END AS personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at
FROM production_jobs
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('assembly_status')::text IS NULL OR assembly_status = sqlc.narg('assembly_status')::text)
  AND (sqlc.narg('finishing_status')::text IS NULL OR finishing_status = sqlc.narg('finishing_status')::text)
  AND (sqlc.narg('qc_status')::text IS NULL OR qc_status = sqlc.narg('qc_status')::text)
  AND (sqlc.narg('packaging_status')::text IS NULL OR packaging_status = sqlc.narg('packaging_status')::text)
  AND (sqlc.narg('order_id')::uuid IS NULL OR order_id = sqlc.narg('order_id')::uuid)
  AND (sqlc.narg('batch_id')::uuid IS NULL OR batch_id = sqlc.narg('batch_id')::uuid)
  -- One box, several fields. An operator looking for a plank has the customer's
  -- names in front of them, or a job number off the plate, or the product - not
  -- a column to choose first. personalisation_name is where BOTH names live,
  -- joined as "HABEEB & FARSANA" by the importer, so either half matches.
  AND (
    sqlc.narg('search')::text IS NULL
    OR job_number ILIKE '%' || sqlc.narg('search')::text || '%'
    OR personalisation_name ILIKE '%' || sqlc.narg('search')::text || '%'
    OR customer_name ILIKE '%' || sqlc.narg('search')::text || '%'
    OR product_name ILIKE '%' || sqlc.narg('search')::text || '%'
  )
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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

-- name: SetProductionJobPrintFile :one
-- Clears the issue reasons this upload is the direct remedy for, and only
-- those. Leaving one set would keep the job out of ListBatchableJobs after an
-- operator did the one thing that fixes it.
--
-- All three mean the same thing in different words - Tensor has no model for
-- this product:
--   stl_missing        a model was expected and is not there
--   no_approved_design nothing in the design tables matches the SKU
--   sku_missing        the line carries no SKU to match on at all
-- An operator uploading the file answers every one of them.
--
-- design_file_stale is the fourth and means something different - there IS a
-- file and the customer changed the order after it was made - but the remedy
-- is the same upload, so it clears here too. This is how a
-- product Tensor does not generate - a photo frame, a night lamp - reaches a
-- bed at all: somebody supplies the 3MF and the job joins the colour it was
-- already recorded against.
--
-- Any other reason survives untouched: an upload does not fix colour_missing
-- or filament_out_of_stock, and blanket-clearing would push a genuinely
-- unvalidated job into batching.
--
-- AND IT CLEARS model_render_queued_at, because attaching a print file is
-- exactly what that column waits for. Every path that stores a model comes
-- through here - GenerateModelForJob, recolourModel, uploadJobModel,
-- setPrintFile - so one clear covers all four and none of them can forget.
--
-- THE COLOURS COME WITH THE FILE, optionally, and in the same statement for a
-- reason: a model and the filaments it declares describe one thing, and
-- writing them apart leaves an instant where the job is batchable by a colour
-- that is not the one the uploaded file asks for. set_colours false leaves
-- both columns exactly as they were, which is what every caller but the
-- upload passes.
UPDATE production_jobs SET
    print_file_id          = sqlc.arg('print_file_id'),
    colours                = CASE WHEN sqlc.arg('set_colours')::bool
                                  THEN sqlc.arg('colours')::jsonb ELSE colours END,
    colour_count           = CASE WHEN sqlc.arg('set_colours')::bool
                                  THEN sqlc.narg('colour_count') ELSE colour_count END,
    issue_reason           = CASE WHEN issue_reason IN ('stl_missing', 'no_approved_design',
                                                        'sku_missing', 'design_file_stale')
                                  THEN NULL ELSE issue_reason END,
    model_render_queued_at = NULL,
    updated_at             = now()
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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

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
  -- A model being rebuilt is about to change. Planning from the one on file
  -- would pack the bed to the old geometry while grouping it by the colour
  -- the customer just chose. Cleared the moment the new file is attached, so
  -- the very next run picks the job up - see migration 0102.
  AND model_render_queued_at IS NULL
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
  AND held = false
  -- Identical to ListBatchableJobs, as the note above promises: counting a job
  -- the list would refuse is how the threshold fires a plan that finds nothing.
  AND model_render_queued_at IS NULL;

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
  -- A model being rebuilt is about to change. Planning from the one on file
  -- would pack the bed to the old geometry while grouping it by the colour
  -- the customer just chose. Cleared the moment the new file is attached, so
  -- the very next run picks the job up - see migration 0102.
  AND model_render_queued_at IS NULL
-- No material, nozzle, quality or machine_family predicate.
--
-- A bed is a COLOUR. Those five narrowed the offer to jobs whose design
-- matched the bed's in every respect, which is how two gold Dual Name Planks
-- differing only by an LED in the box ended up unable to share a plate. The
-- printer is chosen when the bed is queued, and the filament the plate is
-- stamped with is settled by batchMaterialFromRows.
--
-- Colour is filtered in Go by the caller rather than here - see
-- listCompatibleJobs, which explains why: the predicate would be a second
-- implementation of NormalisedColourKey in another language, and Go sorts by
-- byte where Postgres sorts by collation.
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
          personalisation_original, part_role,
          model_error, model_error_at, issue_reason, bbox_x_mm, bbox_y_mm, bbox_z_mm,
          support_weight_g, purge_weight_g, colour_count, created_at, updated_at, bulk_order_id, model_render_queued_at;

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
-- b.manual excludes a bed somebody built by hand. Its jobs sit on a Draft, so
-- without this they would be reconsidered, the bed dissolved and the planks
-- handed back out - undoing a person's deliberate arrangement on a timer.
WHERE (j.batch_id IS NULL OR (b.status = 'pending_approval' AND b.manual = false))
  AND j.status = 'queued'
  AND j.quantity > 0
  AND j.personalisation_status IN ('validated', 'not_required')
  AND j.issue_reason IS NULL
  AND j.held = false
  -- See ListBatchableJobs: a model mid-rebuild is not the model that will
  -- print. This is the planner's only input, so this is the line that makes
  -- "what reaches a bed is what the customer last approved" true.
  AND j.model_render_queued_at IS NULL
ORDER BY COALESCE(o.placed_at, j.created_at) ASC, j.job_number ASC, j.id ASC;

-- name: RankJobsForPriorityOrder :execrows
-- Stamps the urgent rank on an order's jobs.
--
-- jobPriorityRank does this when a job is CREATED, which only covers jobs made
-- after the order was known to be priority. It missed every job that already
-- existed - 21 of 32 on the live database - and would miss any order whose
-- shipping option is only learned on a later sync. Run on every import, it
-- makes the rank self-healing rather than something a one-off command has to
-- repair.
--
-- Only ever makes a job MORE urgent: a line escalated by hand below
-- PriorityRank keeps the rank somebody gave it deliberately. Jobs already at or
-- below the rank are untouched, so this is a no-op on the common path and does
-- not churn updated_at.
UPDATE production_jobs SET priority = sqlc.arg('rank')::int, updated_at = now()
WHERE order_id = sqlc.arg('order_id') AND priority > sqlc.arg('rank')::int;

-- name: AssignUnbatchedJobsWithinCap :execrows
-- Puts UNBATCHED jobs on a bed, refusing the whole statement if the units being
-- added would take it past its cap.
--
-- The capacity rule lives in the write, not in a check before it: two callers
-- that each saw one free place would otherwise both succeed and put five
-- products on a bed of four. The CTEs and the UPDATE share one statement
-- snapshot, so the arithmetic is self-consistent; pair it with LockBatchRow to
-- stop two transactions racing.
--
-- Takes jobs that are unbatched OR on a Draft, never off a locked bed: a Draft
-- is a proposal the planner rebuilds every run, so moving a plank out of one
-- costs nothing, while a locked bed is a committed plate. Zero rows back means
-- somebody claimed the jobs first, which the caller treats as "skip", not an
-- error.
WITH used AS (
    SELECT coalesce(sum(quantity), 0)::int AS n
      FROM production_jobs WHERE batch_id = sqlc.arg('batch_id')
), movable AS (
    SELECT j.id, j.quantity FROM production_jobs j
      LEFT JOIN batches b ON b.id = j.batch_id
     WHERE j.id = ANY(sqlc.arg('job_ids')::uuid[])
       AND (j.batch_id IS NULL OR (b.status = 'pending_approval' AND b.manual = false))
), adding AS (
    SELECT coalesce(sum(quantity), 0)::int AS n FROM movable
)
UPDATE production_jobs SET batch_id = sqlc.arg('batch_id'), updated_at = now()
WHERE id IN (SELECT id FROM movable)
  AND (SELECT n FROM used) + (SELECT n FROM adding) <= sqlc.arg('max_units')::int;

-- name: UnassignJobsFromBatches :exec
-- Detaches jobs from the given batches so they can be re-planned. Guarded on
-- the batch still being a Draft, so a batch approved between the planner
-- reading the pool and writing its result keeps every one of its jobs.
UPDATE production_jobs SET batch_id = NULL, updated_at = now()
WHERE batch_id = ANY(sqlc.arg('batch_ids')::uuid[])
  AND batch_id IN (SELECT id FROM batches WHERE status = 'pending_approval');

-- name: ReleaseJobsFromBatch :exec
-- Puts one bed's jobs back in the pool, whatever the bed's status.
--
-- UnassignJobsFromBatches above is Draft-guarded for the planner's sake. This is
-- for deleting a bed on purpose: a locked bed's jobs are still real work that
-- somebody is waiting for, and dropping the bed without freeing them would
-- strand them - assigned to a batch that no longer exists, invisible to the
-- planner, waiting for a plate nobody will ever print.
UPDATE production_jobs SET batch_id = NULL, updated_at = now()
WHERE batch_id = sqlc.arg('batch_id');

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
    -- The exact complement of ListBatchableJobs, which is the whole value of
    -- this query: a job excluded there and missing here is a job nobody can
    -- find out about.
    OR model_render_queued_at IS NOT NULL
  )
ORDER BY created_at ASC, id ASC;

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

-- name: SearchJobsForCustomBatch :many
-- Jobs a person may put on a bed they are building by hand, by search.
--
-- EVERY job, not only the ones on an unfulfilled order. This was restricted to
-- outstanding orders on the reasoning that a bed is built to clear work that is
-- waiting - true for most beds, and wrong for the ones somebody opens this
-- dialog for: a plank that printed badly last week, a customer asking for a
-- second copy, a job whose order shipped without it. Those are exactly the
-- cases where the job number is already known and typed.
--
-- Filtered in SQL rather than in the browser: the pool grows with every order
-- ever placed, and sending all of it so the browser can hide most of it turns a
-- search box into a download. An empty search returns the head of the list,
-- which is what the dialog offers before anybody types.
--
-- Ordered outstanding first, then by when the CUSTOMER placed the order, oldest
-- first, matching the planner - jobs created by one import share a created_at
-- to the microsecond, so ordering on that would list a ninety-order import
-- arbitrarily.
--
-- j.* alone, so this returns production_jobs rows rather than a bespoke shape.
-- The bed's status and number are read separately by id: two small queries beat
-- a joined row that has to be copied field by field back into a job, where a
-- column added later would silently arrive as a zero value.
SELECT j.* FROM production_jobs j
LEFT JOIN orders o ON o.id = j.order_id
-- A job with nothing left to print is spent: a split job's original row sits at
-- zero once every unit has been peeled off into rows of its own.
WHERE j.quantity > 0
  AND (
    sqlc.narg('search')::text IS NULL
    OR j.job_number ILIKE '%' || sqlc.narg('search')::text || '%'
    OR COALESCE(o.order_number, '') ILIKE '%' || sqlc.narg('search')::text || '%'
    OR COALESCE(j.product_name, '') ILIKE '%' || sqlc.narg('search')::text || '%'
  )
ORDER BY (o.fulfillment_status IS DISTINCT FROM 'fulfilled') DESC,
         COALESCE(o.placed_at, j.created_at) ASC, j.job_number ASC, j.id ASC
LIMIT sqlc.arg('row_limit');

-- name: RequeueFinishedJob :exec
-- Puts a finished plank back in the queue so it can be printed again.
--
-- The same job, not a copy. A reprint elsewhere in the service mints a new row
-- because it is recording a FAILURE - the original really did print, badly, and
-- that has to stay on the record. This is somebody deliberately putting a plank
-- back on a bed, and they want the job they already have: its number, its
-- order, its model, its personalisation. A second row would leave the floor
-- reconciling two job numbers for one plank.
--
-- Its stages reset with it. A plank about to be printed again has not passed
-- the QC it passed last time, and leaving qc_status on 'passed' would carry a
-- verdict about a plank that no longer exists onto one not yet made.
-- assembly_status keeps 'not_required', which is a fact about the product
-- rather than about this print.
--
-- Guarded on completed: this is not a general-purpose way to un-finish a job,
-- and a caller that reaches it with anything else has a bug worth failing on
-- rather than a row worth rewriting.
UPDATE production_jobs SET
    status           = 'queued',
    qc_status        = 'pending',
    packaging_status = 'pending',
    assembly_status  = CASE WHEN assembly_status = 'not_required'
                            THEN assembly_status ELSE 'pending' END,
    updated_at       = now()
WHERE id = sqlc.arg('id') AND status = 'completed';

-- name: MoveJobsToBatch :execrows
-- Puts jobs on a bed somebody is building by hand, including jobs taken off a
-- LOCKED bed.
--
-- AssignJobsToBatch, which the planner uses, allows only unassigned jobs and
-- jobs on a Draft - and rightly: the planner writes from a pool it read moments
-- earlier, so without that guard a plan could steal the contents of a plate
-- whose filament is reserved and which a machine is about to print.
--
-- A person choosing a plank is not that. They can see the bed they are taking
-- it from, and the caller has already released it properly: its plate is out of
-- BambuBuddy's queue and its filament given back (beginBatchEdit), and it is
-- rebuilt from what remains afterwards (finishBatchEdit). Only in_progress and
-- completed stay untouchable, because those record what physically happened to
-- a plate.
--
-- :execrows, not :exec. The silent version was the bug this replaces: every
-- plank was refused by the Draft-only guard, nothing moved, and the caller
-- happily reported a new bed holding nothing at all.
UPDATE production_jobs SET batch_id = sqlc.arg('batch_id'), updated_at = now()
WHERE id = ANY(sqlc.arg('job_ids')::uuid[])
  AND (batch_id IS NULL
       OR batch_id IN (SELECT id FROM batches
                        WHERE status IN ('pending_approval', 'open')));

-- name: ReleaseOrderPersonalisationToPrint :exec
-- Lets an order's jobs through to batching once the customer's window is over,
-- whether they confirmed or it simply ran out.
--
-- Scoped to awaiting_customer, so it cannot disturb a job that a person in the
-- shop is still checking by hand, nor re-stamp one already validated.
UPDATE production_jobs
SET personalisation_status       = 'validated',
    personalisation_validated_by = sqlc.arg('validated_by'),
    personalisation_validated_at = now()
WHERE order_id = sqlc.arg('order_id')
  AND personalisation_status = 'awaiting_customer';

-- name: ListProductionJobsForOrder :many
-- Every job an order produced, oldest first, for the customer's own page.
SELECT * FROM production_jobs
WHERE order_id = sqlc.arg('order_id')
ORDER BY created_at, job_number;

-- name: SaveCustomerPersonalisation :execrows
-- Writes what the customer changed on their own link.
--
-- GUARDED ON awaiting_customer, which is the whole safety of the edit path.
-- It means an edit can only land while that customer's window is open: not
-- after it closed, not after they confirmed, not on a job somebody in the shop
-- already validated, and never on a job that has reached a bed. Returning the
-- row count rather than the row lets the caller tell "refused" from "saved"
-- without a second read.
--
-- personalisation_original is written ONCE. coalesce keeps whatever is already
-- there, so a second edit cannot overwrite the values the order arrived with -
-- which would make the customer's own first correction look like what they
-- bought.
--
-- model_render_queued_at IS STAMPED HERE, unconditionally and in the same
-- statement as the values. The claim it makes is true of both kinds of edit -
-- the model on file is now older than the order - and writing it alongside
-- leaves no instant where the new values are saved and the hold is not. A
-- branch on "did the text change?" would also miss the colour-only
-- fall-through, where recolourModel declines and the render is queued anyway.
-- It is cleared by SetProductionJobPrintFile when the new model lands.
UPDATE production_jobs
SET personalisation_properties = sqlc.arg('properties'),
    personalisation_colour     = sqlc.narg('colour'),
    colours                    = sqlc.arg('colours'),
    personalisation_original   = coalesce(personalisation_original, sqlc.arg('original')),
    model_render_queued_at     = now(),
    updated_at                 = now()
WHERE id = sqlc.arg('id')
  AND personalisation_status = 'awaiting_customer';

-- name: ReleaseStrandedPersonalisationHolds :execrows
-- Frees jobs held for a link that never arrived.
--
-- THE BACKSTOP, and the reason the whole design is safe to run unattended.
-- The renderer holds a job before the sender has run, so between those two
-- moments a crash, a lost trigger or a refusal nobody handled would leave a
-- job waiting for a customer who was never asked - and the expiry sweep walks
-- LINKS, so it would never see it.
--
-- Scoped to jobs whose order has no link at all. A job whose link exists is
-- the expiry sweep's business, whether that link is open or already closed;
-- this only takes the ones nothing else can reach.
--
-- The age bound is what keeps it from racing the sender: a model lands, the
-- job is held, and the send is queued behind it. Anything still stranded well
-- after that was never going to be asked.
UPDATE production_jobs j
SET personalisation_status       = 'validated',
    personalisation_validated_by = 'system (never asked)',
    personalisation_validated_at = now()
WHERE j.personalisation_status = 'awaiting_customer'
  AND j.updated_at < now() - sqlc.arg('stranded_after')::interval
  AND NOT EXISTS (
      SELECT 1 FROM personalisation_links l WHERE l.order_id = j.order_id
  );

-- name: HoldOrderPersonalisationForCustomer :execrows
-- Holds the REST of an order's personalised jobs while the customer's window
-- is open.
--
-- The renderer holds what it renders (confirmGeneratedPersonalisation), which
-- covers the plank family and anything the registry describes. It cannot hold
-- what it never renders - a Bone Tag, a DUOSNAP, a Couple's Date Plank - and
-- those jobs were therefore shown on the customer's page with every field
-- locked, because the edit path is guarded on awaiting_customer. A page of
-- read-only boxes is not a check.
--
-- Run when the link is actually SENT, not when the jobs are made. That is what
-- keeps the invariant the hold depends on: every held job has a live link, so
-- the expiry sweep and the confirm both find it and release it. Holding at
-- creation would strand an order that never gets a link.
--
-- NEVER TOUCHES A JOB ALREADY COMMITTED. A job on a bed, or one past the
-- queue, is work in progress: pulling it back into a customer check would take
-- it out of batching under a plate that already counts it.
UPDATE production_jobs
SET personalisation_status = 'awaiting_customer',
    updated_at             = now()
WHERE order_id = sqlc.arg('order_id')
  AND batch_id IS NULL
  AND status IN ('pending', 'queued')
  AND personalisation_status IN ('pending', 'validated', 'not_required')
  AND jsonb_array_length(coalesce(personalisation_properties, '[]'::jsonb)) > 0;

-- name: ClearJobModelRenderHold :exec
-- Releases a job whose render is not coming after all.
--
-- For the two paths that save an edit and then find they cannot queue a
-- render - no enqueuer configured, or the insert failed - and for a render
-- that failed outright. The values are saved either way; holding the job for
-- a rebuild that will never happen would strand it until the sweep.
UPDATE production_jobs
SET model_render_queued_at = NULL, updated_at = now()
WHERE id = sqlc.arg('id') AND model_render_queued_at IS NOT NULL;

-- name: ClearStalledModelRenderHolds :execrows
-- The backstop: frees anything still waiting on a render long after one could
-- plausibly still be running.
--
-- A render is one to two seconds now, so a hold minutes old is a worker that
-- died between queueing and storing, not a slow day. Without this such a job
-- waits for ever, because the thing that would clear it is the thing that did
-- not happen. Returns the count so the sweep can say so out loud.
UPDATE production_jobs
SET model_render_queued_at = NULL, updated_at = now()
WHERE model_render_queued_at IS NOT NULL
  AND model_render_queued_at < now() - sqlc.arg('stalled_after')::interval;

-- name: MarkJobDesignFileStale :execrows
-- Says that the customer changed this item after its design file was made.
--
-- Set when the window CLOSES, never while it is open: until then the customer
-- can still change their mind, and a designer sent to remake a file mid-window
-- could remake it from values that then move again.
--
-- issue_reason IS NULL so it cannot mask something worse - a job already out
-- of filament has a more urgent problem than a rename. print_file_id IS NOT
-- NULL so this only ever means stale, never missing; a job with no file is
-- already saying so, and the design queue finds it by that.
--
-- Both predicates are restated here as well as in the caller on purpose: the
-- statement has to be safe on its own, because a second caller will exist.
UPDATE production_jobs
SET issue_reason           = 'design_file_stale',
    model_render_queued_at = NULL,
    updated_at             = now()
WHERE id = sqlc.arg('id')
  AND issue_reason IS NULL
  AND print_file_id IS NOT NULL;
