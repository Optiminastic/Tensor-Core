-- Station records: assembly, finishing, QC, packaging. Assembly, finishing and
-- QC are append-only; packaging is one row per job (re-packaging updates it).
-- All boolean/text, so the queries return the model types directly.

-- name: InsertFinishingCheck :one
INSERT INTO production_job_finishing_checks (
    id, job_id, supports_removed, sanded, seams_cleaned, surface_finish_ok,
    photo_file_id, notes, finished_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('supports_removed'), sqlc.arg('sanded'),
    sqlc.arg('seams_cleaned'), sqlc.arg('surface_finish_ok'), sqlc.narg('photo_file_id'),
    sqlc.narg('notes'), sqlc.arg('finished_by')
)
RETURNING *;

-- name: InsertAssemblyCheck :one
INSERT INTO production_job_assembly_checks (
    id, job_id, parts_combined, hardware_attached, addons_attached, fit_check_ok,
    photo_file_id, notes, assembled_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('parts_combined'), sqlc.arg('hardware_attached'),
    sqlc.arg('addons_attached'), sqlc.arg('fit_check_ok'), sqlc.narg('photo_file_id'),
    sqlc.narg('notes'), sqlc.arg('assembled_by')
)
RETURNING *;

-- name: InsertQcCheck :one
INSERT INTO production_job_qc_checks (
    id, job_id, correct_personalisation, correct_colour, surface_finish_ok, no_cracks,
    no_layer_defects, dimensions_ok, assembly_fit_ok, addons_working, packaging_safe,
    photo_file_id, decision, notes, inspected_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('correct_personalisation'), sqlc.arg('correct_colour'),
    sqlc.arg('surface_finish_ok'), sqlc.arg('no_cracks'), sqlc.arg('no_layer_defects'),
    sqlc.arg('dimensions_ok'), sqlc.arg('assembly_fit_ok'), sqlc.arg('addons_working'),
    sqlc.arg('packaging_safe'), sqlc.narg('photo_file_id'), sqlc.arg('decision'),
    sqlc.narg('notes'), sqlc.arg('inspected_by')
)
RETURNING *;

-- name: UpsertPackagingDetail :one
INSERT INTO production_job_packaging_details (
    id, job_id, packaging_type, addons, gift_message, fragile, courier_partner,
    invoice_reference, photo_file_id, packed_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('packaging_type'), sqlc.narg('addons'),
    sqlc.narg('gift_message'), sqlc.arg('fragile'), sqlc.narg('courier_partner'),
    sqlc.narg('invoice_reference'), sqlc.narg('photo_file_id'), sqlc.arg('packed_by')
)
ON CONFLICT (job_id) DO UPDATE SET
    packaging_type    = EXCLUDED.packaging_type,
    addons            = EXCLUDED.addons,
    gift_message      = EXCLUDED.gift_message,
    fragile           = EXCLUDED.fragile,
    courier_partner   = EXCLUDED.courier_partner,
    invoice_reference = EXCLUDED.invoice_reference,
    photo_file_id     = EXCLUDED.photo_file_id,
    packed_by         = EXCLUDED.packed_by,
    packed_at         = now()
RETURNING *;

-- The guarded station transitions. Each folds the prior-stage check INTO the
-- UPDATE's WHERE clause, so check-and-act is one statement and cannot race:
-- a second concurrent (or double-clicked) call matches no row and comes back
-- as pgx.ErrNoRows, which the handler turns into a 409. This is the same shape
-- ApproveBatch uses, and the reason it survives a lost race.
-- Zero rows means "already left this station", never "does not exist" - the
-- handler pre-reads for the 404.

-- name: AdvanceJobAssembly :one
UPDATE production_jobs SET assembly_status = sqlc.arg('assembly_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'completed' AND assembly_status = 'pending'
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

-- name: AdvanceJobFinishing :one
UPDATE production_jobs SET finishing_status = sqlc.arg('finishing_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'completed'
  AND assembly_status <> 'pending' AND finishing_status = 'pending'
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

-- name: AdvanceJobQc :one
UPDATE production_jobs SET qc_status = sqlc.arg('qc_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'completed'
  AND assembly_status <> 'pending' AND finishing_status <> 'pending' AND qc_status = 'pending'
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

-- name: AdvanceJobPackaging :one
UPDATE production_jobs SET packaging_status = sqlc.arg('packaging_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND qc_status = 'passed' AND packaging_status = 'pending'
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
