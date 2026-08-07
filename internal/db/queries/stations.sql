-- Station records: assembly, QC, packaging. Assembly and QC are append-only;
-- packaging is one row per job (re-packaging updates it). All boolean/text, so the
-- queries return the model types directly.

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
