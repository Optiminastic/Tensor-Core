-- name: ListSlicingProfiles :many
SELECT * FROM slicing_profiles ORDER BY upper(machine_family), lower(name);

-- name: GetSlicingProfile :one
SELECT * FROM slicing_profiles WHERE id = $1;

-- name: GetSlicingProfileForPipeline :one
-- The profile mirroring one BambuBuddy pipeline: the slice path's lookup,
-- reached through the SKU's pipeline mapping.
SELECT * FROM slicing_profiles WHERE bambu_pipeline_id = sqlc.arg('pipeline_id')::integer;

-- name: UpsertSlicingProfileFromPipeline :one
-- Import or refresh one pipeline's presets. Overrides survive a refresh, and
-- the version only moves when something actually changed, so re-importing an
-- unchanged pipeline does not make every bed look re-configured.
INSERT INTO slicing_profiles (
    id, bambu_pipeline_id, name, machine_family,
    printer_preset_name, printer_preset, process_preset_name, process_preset,
    filament_preset_name, filament_preset, imported_at, updated_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('bambu_pipeline_id'), sqlc.arg('name'), sqlc.arg('machine_family'),
    sqlc.arg('printer_preset_name'), sqlc.arg('printer_preset'),
    sqlc.arg('process_preset_name'), sqlc.arg('process_preset'),
    sqlc.arg('filament_preset_name'), sqlc.arg('filament_preset'), now(), sqlc.arg('updated_by')
)
ON CONFLICT (bambu_pipeline_id) WHERE bambu_pipeline_id IS NOT NULL DO UPDATE SET
    name                 = EXCLUDED.name,
    machine_family       = EXCLUDED.machine_family,
    printer_preset_name  = EXCLUDED.printer_preset_name,
    printer_preset       = EXCLUDED.printer_preset,
    process_preset_name  = EXCLUDED.process_preset_name,
    process_preset       = EXCLUDED.process_preset,
    filament_preset_name = EXCLUDED.filament_preset_name,
    filament_preset      = EXCLUDED.filament_preset,
    imported_at          = now(),
    updated_by           = EXCLUDED.updated_by,
    version = slicing_profiles.version + CASE WHEN
        (slicing_profiles.printer_preset, slicing_profiles.process_preset, slicing_profiles.filament_preset,
         slicing_profiles.name, slicing_profiles.machine_family)
        IS DISTINCT FROM
        (EXCLUDED.printer_preset, EXCLUDED.process_preset, EXCLUDED.filament_preset,
         EXCLUDED.name, EXCLUDED.machine_family)
        THEN 1 ELSE 0 END,
    updated_at = now()
RETURNING *;

-- name: UpdateSlicingProfileOverrides :one
UPDATE slicing_profiles SET
    overrides  = sqlc.arg('overrides'),
    version    = version + 1,
    updated_by = sqlc.arg('updated_by'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;
