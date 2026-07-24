-- The design pipeline queries. Numeric columns cast to/from float8 (as in
-- brands.sql) so handlers work in plain float64; json columns are []byte.

-- name: InsertDesign :one
INSERT INTO designs (
    id, brand_slug, name, created_by, status, stl_key,
    material, colour, finish, units_per_bed, quality, infill_pct
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('name'), sqlc.arg('created_by'),
    sqlc.arg('status'), sqlc.arg('stl_key'), sqlc.arg('material'), sqlc.narg('colour'),
    sqlc.arg('finish'), sqlc.arg('units_per_bed'), sqlc.arg('quality'),
    sqlc.arg('infill_pct')::float8
)
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
          created_at, updated_at;

-- name: GetDesignByID :one
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       created_at, updated_at
FROM designs WHERE id = $1;

-- name: ListDesignsByBrand :many
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       created_at, updated_at
FROM designs WHERE brand_slug = $1 ORDER BY created_at DESC;

-- name: UpdateDesignStatus :exec
UPDATE designs SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UpdateDesignSpecs :exec
UPDATE designs SET
    material = sqlc.arg('material'), colour = sqlc.narg('colour'),
    finish = sqlc.arg('finish'), units_per_bed = sqlc.arg('units_per_bed'),
    quality = sqlc.arg('quality'), infill_pct = sqlc.arg('infill_pct')::float8,
    status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: GetLatestJobForDesign :one
SELECT id, design_id, status, attempt, error, created_at, updated_at
FROM slice_jobs WHERE design_id = $1 ORDER BY attempt DESC LIMIT 1;

-- name: InsertSliceJob :one
INSERT INTO slice_jobs (id, design_id, status, attempt)
VALUES (sqlc.arg('id'), sqlc.arg('design_id'), sqlc.arg('status'), sqlc.arg('attempt'))
RETURNING id, design_id, status, attempt, error, created_at, updated_at;

-- name: GetSliceJobByID :one
SELECT id, design_id, status, attempt, error, created_at, updated_at
FROM slice_jobs WHERE id = $1;

-- name: NextAttemptForDesign :one
SELECT COALESCE(MAX(attempt), 0) + 1 AS next_attempt
FROM slice_jobs WHERE design_id = $1;

-- name: UpdateSliceJobStatus :exec
UPDATE slice_jobs
SET status = sqlc.arg('status'), error = sqlc.narg('error'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: InsertSliceMetrics :exec
INSERT INTO slice_metrics (
    job_id, print_time_hr, effective_machine_time_hr, filament_g, purge_g, support_g,
    colour_changes, electricity_kwh, units_per_bed, layer_height_mm, infill_density_pct,
    wall_loops, support_used, filament_length_mm, gcode_key
) VALUES (
    sqlc.arg('job_id'), sqlc.arg('print_time_hr')::float8,
    sqlc.arg('effective_machine_time_hr')::float8, sqlc.arg('filament_g')::float8,
    sqlc.arg('purge_g')::float8, sqlc.arg('support_g')::float8, sqlc.arg('colour_changes'),
    sqlc.arg('electricity_kwh')::float8, sqlc.arg('units_per_bed'),
    sqlc.arg('layer_height_mm')::float8, sqlc.arg('infill_density_pct')::float8,
    sqlc.arg('wall_loops'), sqlc.arg('support_used'), sqlc.arg('filament_length_mm')::float8,
    sqlc.arg('gcode_key')
);

-- name: GetLatestMetricsForDesign :one
SELECT m.job_id, m.print_time_hr::float8 AS print_time_hr,
       m.effective_machine_time_hr::float8 AS effective_machine_time_hr,
       m.filament_g::float8 AS filament_g, m.purge_g::float8 AS purge_g,
       m.support_g::float8 AS support_g, m.colour_changes,
       m.electricity_kwh::float8 AS electricity_kwh, m.units_per_bed,
       m.layer_height_mm::float8 AS layer_height_mm,
       m.infill_density_pct::float8 AS infill_density_pct, m.wall_loops,
       m.support_used, m.filament_length_mm::float8 AS filament_length_mm,
       m.gcode_key, m.created_at
FROM slice_metrics m
JOIN slice_jobs j ON j.id = m.job_id
WHERE j.design_id = $1
ORDER BY j.attempt DESC
LIMIT 1;

-- name: UpsertDesignPricing :exec
INSERT INTO design_pricing (
    design_id, design_cp, breakdown, verdict, cp_pct, recommended_sp, reasons, suggestions
) VALUES (
    sqlc.arg('design_id'), sqlc.arg('design_cp')::float8, sqlc.arg('breakdown')::json,
    sqlc.arg('verdict'), sqlc.arg('cp_pct')::float8, sqlc.narg('recommended_sp'),
    sqlc.arg('reasons')::json, sqlc.arg('suggestions')::json
)
ON CONFLICT (design_id) DO UPDATE SET
    design_cp = EXCLUDED.design_cp, breakdown = EXCLUDED.breakdown,
    verdict = EXCLUDED.verdict, cp_pct = EXCLUDED.cp_pct,
    recommended_sp = EXCLUDED.recommended_sp, reasons = EXCLUDED.reasons,
    suggestions = EXCLUDED.suggestions, updated_at = now();

-- name: GetDesignPricing :one
SELECT design_id, design_cp::float8 AS design_cp, breakdown, verdict,
       cp_pct::float8 AS cp_pct, recommended_sp, reasons, suggestions,
       created_at, updated_at
FROM design_pricing WHERE design_id = $1;
