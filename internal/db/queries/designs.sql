-- The design pipeline queries. Numeric columns cast to/from float8 (as in
-- brands.sql) so handlers work in plain float64; json columns are []byte.

-- name: InsertDesign :one
INSERT INTO designs (
    id, brand_slug, name, created_by, status, stl_key,
    material, colour, finish, units_per_bed, quality, infill_pct, machine_id
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('name'), sqlc.arg('created_by'),
    sqlc.arg('status'), sqlc.arg('stl_key'), sqlc.arg('material'), sqlc.narg('colour'),
    sqlc.arg('finish'), sqlc.arg('units_per_bed'), sqlc.arg('quality'),
    sqlc.arg('infill_pct')::float8, sqlc.narg('machine_id')
)
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
          sku, machine_id, created_at, updated_at;

-- name: GetDesignByID :one
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       sku, machine_id, created_at, updated_at
FROM designs WHERE id = $1;

-- name: ListDesignsByBrand :many
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       sku, machine_id, created_at, updated_at
FROM designs WHERE brand_slug = $1 ORDER BY created_at DESC;

-- name: ListDesignsByBrandPage :many
-- Keyset page: rows strictly before the (created_at, id) cursor, newest first. A
-- null cursor returns the first page. Ordering matches ListDesignsByBrand with id
-- as a stable tiebreaker.
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       sku, machine_id, created_at, updated_at
FROM designs
WHERE brand_slug = sqlc.arg('brand_slug')
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: UpdateDesignStatus :exec
UPDATE designs SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UpdateDesignSku :one
-- A nil sku clears it (frontend contract: empty string -> clear). The caller
-- maps a unique_violation on this to a 409 - see designs.go#setDesignSku.
UPDATE designs SET sku = sqlc.narg('sku'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
          sku, machine_id, created_at, updated_at;

-- name: UpdateDesignMachine :one
-- Relinks a design to a different machine_profiles row (see
-- internal/httpapi/design_machine_link.go#findOrCreateMachineProfile) - the
-- "Machine" tab's edit form.
UPDATE designs SET machine_id = sqlc.arg('machine_id'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
          sku, machine_id, created_at, updated_at;

-- name: GetDesignBySKU :one
-- Used by the job-creation worker to match an order line item's SKU to its
-- approved design. Only approved/published designs are sellable/printable.
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       sku, machine_id, created_at, updated_at
FROM designs WHERE sku = $1 AND status IN ('approved', 'published');

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

-- name: UpsertSliceMetrics :exec
-- Idempotent on job_id (the primary key), because a River retry re-runs the
-- whole slice for the same domain job id. Without this, a crash after
-- ProcessSliceResult committed but before River acked the job left every
-- remaining attempt unique-violating on this insert - burning the attempts and
-- getting discarded, while the design was already correctly priced.
--
-- DO UPDATE rather than DO NOTHING, deliberately: priceDesign prices from the
-- in-memory PerUnitMetrics, not from this row. Keeping the first attempt's row
-- while re-deriving the price from the second attempt's numbers would leave the
-- stored slicer facts and the price describing different slices, which breaks
-- "slicer output is the source of truth". created_at deliberately keeps the
-- first attempt's timestamp - the row describes a design, not an attempt.
INSERT INTO slice_metrics (
    job_id, print_time_hr, effective_machine_time_hr, filament_g, purge_g, support_g,
    colour_changes, electricity_kwh, units_per_bed, layer_height_mm, infill_density_pct,
    wall_loops, support_used, filament_length_mm, gcode_key, orientation
) VALUES (
    sqlc.arg('job_id'), sqlc.arg('print_time_hr')::float8,
    sqlc.arg('effective_machine_time_hr')::float8, sqlc.arg('filament_g')::float8,
    sqlc.arg('purge_g')::float8, sqlc.arg('support_g')::float8, sqlc.arg('colour_changes'),
    sqlc.arg('electricity_kwh')::float8, sqlc.arg('units_per_bed'),
    sqlc.arg('layer_height_mm')::float8, sqlc.arg('infill_density_pct')::float8,
    sqlc.arg('wall_loops'), sqlc.arg('support_used'), sqlc.arg('filament_length_mm')::float8,
    sqlc.arg('gcode_key'), sqlc.narg('orientation')::jsonb
)
ON CONFLICT (job_id) DO UPDATE SET
    print_time_hr             = EXCLUDED.print_time_hr,
    effective_machine_time_hr = EXCLUDED.effective_machine_time_hr,
    filament_g                = EXCLUDED.filament_g,
    purge_g                   = EXCLUDED.purge_g,
    support_g                 = EXCLUDED.support_g,
    colour_changes            = EXCLUDED.colour_changes,
    electricity_kwh           = EXCLUDED.electricity_kwh,
    units_per_bed             = EXCLUDED.units_per_bed,
    layer_height_mm           = EXCLUDED.layer_height_mm,
    infill_density_pct        = EXCLUDED.infill_density_pct,
    wall_loops                = EXCLUDED.wall_loops,
    support_used              = EXCLUDED.support_used,
    filament_length_mm        = EXCLUDED.filament_length_mm,
    gcode_key                 = EXCLUDED.gcode_key,
    orientation               = EXCLUDED.orientation;

-- name: GetLatestMetricsForDesign :one
SELECT m.job_id, m.print_time_hr::float8 AS print_time_hr,
       m.effective_machine_time_hr::float8 AS effective_machine_time_hr,
       m.filament_g::float8 AS filament_g, m.purge_g::float8 AS purge_g,
       m.support_g::float8 AS support_g, m.colour_changes,
       m.electricity_kwh::float8 AS electricity_kwh, m.units_per_bed,
       m.layer_height_mm::float8 AS layer_height_mm,
       m.infill_density_pct::float8 AS infill_density_pct, m.wall_loops,
       m.support_used, m.filament_length_mm::float8 AS filament_length_mm,
       m.gcode_key, m.orientation, m.created_at
FROM slice_metrics m
JOIN slice_jobs j ON j.id = m.job_id
WHERE j.design_id = $1
ORDER BY j.attempt DESC
LIMIT 1;

-- name: UpsertDesignPricing :exec
INSERT INTO design_pricing (
    design_id, design_cp, breakdown, verdict, cp_pct, recommended_sp,
    raw_sp, cp_pct_at_recommended, passes_normal, survives_stress, sp_warnings,
    reasons, suggestions
) VALUES (
    sqlc.arg('design_id'), sqlc.arg('design_cp')::float8, sqlc.arg('breakdown')::json,
    sqlc.arg('verdict'), sqlc.arg('cp_pct')::float8, sqlc.narg('recommended_sp'),
    sqlc.arg('raw_sp')::float8, sqlc.narg('cp_pct_at_recommended')::float8,
    sqlc.arg('passes_normal'), sqlc.arg('survives_stress'), sqlc.arg('sp_warnings')::json,
    sqlc.arg('reasons')::json, sqlc.arg('suggestions')::json
)
ON CONFLICT (design_id) DO UPDATE SET
    design_cp = EXCLUDED.design_cp, breakdown = EXCLUDED.breakdown,
    verdict = EXCLUDED.verdict, cp_pct = EXCLUDED.cp_pct,
    recommended_sp = EXCLUDED.recommended_sp, raw_sp = EXCLUDED.raw_sp,
    cp_pct_at_recommended = EXCLUDED.cp_pct_at_recommended,
    passes_normal = EXCLUDED.passes_normal, survives_stress = EXCLUDED.survives_stress,
    sp_warnings = EXCLUDED.sp_warnings, reasons = EXCLUDED.reasons,
    suggestions = EXCLUDED.suggestions, updated_at = now();

-- name: GetDesignPricing :one
SELECT design_id, design_cp::float8 AS design_cp, breakdown, verdict,
       cp_pct::float8 AS cp_pct, recommended_sp,
       raw_sp::float8 AS raw_sp, cp_pct_at_recommended::float8 AS cp_pct_at_recommended,
       passes_normal, survives_stress, sp_warnings, reasons, suggestions,
       approved_sp, approved_by, approved_at,
       created_at, updated_at
FROM design_pricing WHERE design_id = $1;

-- name: ApproveDesignPricing :exec
UPDATE design_pricing SET
    approved_sp = sqlc.narg('approved_sp'),
    approved_by = sqlc.narg('approved_by'),
    approved_at = now(),
    updated_at = now()
WHERE design_id = sqlc.arg('design_id');

-- name: UpsertShopifyProduct :exec
INSERT INTO shopify_products (
    design_id, brand_slug, product_gid, variant_gid, handle, admin_url, status, published_by
) VALUES (
    sqlc.arg('design_id'), sqlc.arg('brand_slug'), sqlc.arg('product_gid'), sqlc.arg('variant_gid'),
    sqlc.arg('handle'), sqlc.arg('admin_url'), sqlc.arg('status'), sqlc.narg('published_by')
)
ON CONFLICT (design_id) DO UPDATE SET
    product_gid = EXCLUDED.product_gid, variant_gid = EXCLUDED.variant_gid, handle = EXCLUDED.handle,
    admin_url = EXCLUDED.admin_url, status = EXCLUDED.status,
    published_by = EXCLUDED.published_by, updated_at = now();

-- name: GetShopifyProduct :one
SELECT design_id, brand_slug, product_gid, variant_gid, handle, admin_url, status,
       published_by, created_at, updated_at
FROM shopify_products WHERE design_id = $1;

-- name: MarkDesignSlicing :exec
-- Flips a design to 'slicing' when a worker picks up its job, but never
-- demotes one that is already priced. A River retry re-runs Work from the top,
-- and this write happens OUTSIDE ProcessSliceResult's transaction - so on a
-- retry after a partial commit it would drag a correctly-priced design back to
-- 'slicing' and, if the retry then failed, strand it there. The guard makes
-- that impossible regardless of what the rest of the attempt does.
UPDATE designs SET status = 'slicing', updated_at = now()
WHERE id = sqlc.arg('id') AND status <> 'priced';
