-- The design pipeline queries. Numeric columns cast to/from float8 (as in
-- brands.sql) so handlers work in plain float64; json columns are []byte.

-- name: InsertDesign :one
INSERT INTO designs (
    id, brand_slug, name, created_by, status, stl_key,
    material, colour, finish, units_per_bed, quality, infill_pct, notes, preview_key, sku, machine_id, attributes
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('name'), sqlc.arg('created_by'),
    sqlc.arg('status'), sqlc.arg('stl_key'), sqlc.arg('material'), sqlc.narg('colour'),
    sqlc.arg('finish'), sqlc.arg('units_per_bed'), sqlc.arg('quality'),
    sqlc.arg('infill_pct')::float8, sqlc.narg('notes'), sqlc.arg('preview_key'), sqlc.narg('sku'),
    sqlc.narg('machine_id'), sqlc.narg('attributes')
)
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct, notes,
          preview_key, sku, created_at, updated_at;

-- NextDesignSkuSeq draws the next value for an auto-generated SKU's numeric suffix.
-- name: NextDesignSkuSeq :one
SELECT nextval('designs_sku_seq')::bigint AS seq;

-- name: GetDesignByID :one
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct, notes,
       preview_key, sku, machine_id, personalisation_rules, attributes,
       personalisation, personalised_stl_key, created_at, updated_at
FROM designs WHERE id = $1;

-- SetDesignPersonalisation stores the applied personalisation spec and the baked
-- STL key for a design (or clears both with NULLs). Returns the updated row.
-- name: SetDesignPersonalisation :one
UPDATE designs
SET personalisation = sqlc.narg('personalisation'),
    personalised_stl_key = sqlc.narg('personalised_stl_key'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct, notes,
          preview_key, sku, machine_id, personalisation_rules, attributes,
          personalisation, personalised_stl_key, created_at, updated_at;

-- GetDesignBrandSlug returns only a design's brand, for the cheap per-request
-- brand-access gate on the /:id sub-routes (no need to load the whole row).
-- name: GetDesignBrandSlug :one
SELECT brand_slug FROM designs WHERE id = $1;

-- DeleteDesign removes a design; every child row (jobs, metrics, pricing, reviews,
-- optimisations, attributes) is ON DELETE CASCADE, so this one statement is enough.
-- name: DeleteDesign :execrows
DELETE FROM designs WHERE id = $1;

-- SetDesignPersonalisationRules stores (or clears, with NULL) the product's
-- personalization rule set. The jsonb shape is validated in Go before it is set.
-- name: SetDesignPersonalisationRules :one
UPDATE designs SET personalisation_rules = sqlc.narg('personalisation_rules'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct, notes,
          preview_key, sku, machine_id, personalisation_rules, created_at, updated_at;

-- name: ListDesignsByBrand :many
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       preview_key, sku, created_at, updated_at
FROM designs WHERE brand_slug = $1 ORDER BY created_at DESC;

-- name: ListDesignsByBrandPage :many
-- Keyset page: rows strictly before the (created_at, id) cursor, newest first. A
-- null cursor returns the first page. Ordering matches ListDesignsByBrand with id
-- as a stable tiebreaker.
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       preview_key, sku, created_at, updated_at
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

-- name: InsertDesignReview :one
INSERT INTO design_reviews (id, design_id, author_id, kind, body)
VALUES (sqlc.arg('id'), sqlc.arg('design_id'), sqlc.arg('author_id'), sqlc.arg('kind'), sqlc.narg('body'))
RETURNING *;

-- name: ListDesignReviews :many
SELECT * FROM design_reviews WHERE design_id = $1 ORDER BY created_at ASC, id ASC;

-- name: UpdateDesignSpecs :exec
UPDATE designs SET
    material = sqlc.arg('material'), colour = sqlc.narg('colour'),
    finish = sqlc.arg('finish'), units_per_bed = sqlc.arg('units_per_bed'),
    quality = sqlc.arg('quality'), infill_pct = sqlc.arg('infill_pct')::float8,
    machine_id = sqlc.narg('machine_id'),
    status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id');

-- SetDesignSku assigns (or clears) the catalog SKU. The partial unique index
-- designs_sku_key rejects a duplicate assigned SKU with a 23505 the handler maps
-- to a 409.
-- name: SetDesignSku :one
UPDATE designs SET sku = sqlc.narg('sku'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, brand_slug, name, created_by, status, stl_key, material, colour,
          finish, units_per_bed, quality, infill_pct::float8 AS infill_pct, notes,
          preview_key, sku, created_at, updated_at;

-- GetDesignBySku resolves an order line's SKU to its design so a production job
-- can be built straight from the catalog (STL + material + colour).
-- name: GetDesignBySku :one
SELECT id, brand_slug, name, created_by, status, stl_key, material, colour,
       finish, units_per_bed, quality, infill_pct::float8 AS infill_pct,
       preview_key, sku, template_file_id, personalisation_rules, created_at, updated_at
FROM designs WHERE sku = $1;

-- SetDesignTemplateFile records the file_asset that stands in for the design's
-- model in the production queue, so it is created once and reused for reprints.
-- name: SetDesignTemplateFile :exec
UPDATE designs SET template_file_id = sqlc.arg('template_file_id'), updated_at = now()
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
    wall_loops, support_used, filament_length_mm, gcode_key, orientation
) VALUES (
    sqlc.arg('job_id'), sqlc.arg('print_time_hr')::float8,
    sqlc.arg('effective_machine_time_hr')::float8, sqlc.arg('filament_g')::float8,
    sqlc.arg('purge_g')::float8, sqlc.arg('support_g')::float8, sqlc.arg('colour_changes'),
    sqlc.arg('electricity_kwh')::float8, sqlc.arg('units_per_bed'),
    sqlc.arg('layer_height_mm')::float8, sqlc.arg('infill_density_pct')::float8,
    sqlc.arg('wall_loops'), sqlc.arg('support_used'), sqlc.arg('filament_length_mm')::float8,
    sqlc.arg('gcode_key'), sqlc.narg('orientation')::jsonb
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
