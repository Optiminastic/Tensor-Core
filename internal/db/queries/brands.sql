-- Brands are free-form, keyed by slug. numeric columns cast to/from float8;
-- ladder stays json ([]byte in Go); entry_machine_hours is read raw so its NULL
-- survives.

-- name: ListBrands :many
SELECT id, slug, name, logo_url, starting_price::float8 AS starting_price,
       shopify_url, description, is_active, ladder,
       cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
       entry_machine_hours, entry_rung, created_at, updated_at
FROM brands ORDER BY name;

-- name: GetBrandBySlug :one
SELECT id, slug, name, logo_url, starting_price::float8 AS starting_price,
       shopify_url, description, is_active, ladder,
       cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
       entry_machine_hours, entry_rung, created_at, updated_at
FROM brands WHERE slug = $1;

-- name: BrandExists :one
SELECT EXISTS (SELECT 1 FROM brands WHERE slug = $1) AS brand_exists;

-- name: InsertBrand :one
INSERT INTO brands (
    id, slug, name, logo_url, starting_price, shopify_url, description, is_active,
    ladder, cp_green_max, cp_yellow_max, entry_machine_hours, entry_rung
) VALUES (
    sqlc.arg('id'), sqlc.arg('slug'), sqlc.arg('name'), sqlc.narg('logo_url'),
    sqlc.arg('starting_price')::float8, sqlc.narg('shopify_url'), sqlc.narg('description'),
    sqlc.arg('is_active'), sqlc.arg('ladder')::json,
    sqlc.arg('cp_green_max')::float8, sqlc.arg('cp_yellow_max')::float8,
    sqlc.narg('entry_machine_hours')::float8, sqlc.narg('entry_rung')
)
RETURNING id, slug, name, logo_url, starting_price::float8 AS starting_price,
          shopify_url, description, is_active, ladder,
          cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
          entry_machine_hours, entry_rung, created_at, updated_at;

-- name: DeleteBrand :execrows
DELETE FROM brands WHERE slug = $1;

-- name: UpdateBrand :one
UPDATE brands SET
    name = COALESCE(sqlc.narg('name'), name),
    logo_url = CASE WHEN sqlc.arg('set_logo_url')::bool
                    THEN sqlc.narg('logo_url') ELSE logo_url END,
    starting_price = COALESCE(sqlc.narg('starting_price')::float8, starting_price),
    shopify_url = CASE WHEN sqlc.arg('set_shopify_url')::bool
                       THEN sqlc.narg('shopify_url') ELSE shopify_url END,
    description = CASE WHEN sqlc.arg('set_description')::bool
                      THEN sqlc.narg('description') ELSE description END,
    is_active = COALESCE(sqlc.narg('is_active'), is_active),
    ladder = COALESCE(sqlc.narg('ladder')::json, ladder),
    cp_green_max = COALESCE(sqlc.narg('cp_green_max')::float8, cp_green_max),
    cp_yellow_max = COALESCE(sqlc.narg('cp_yellow_max')::float8, cp_yellow_max),
    entry_machine_hours = CASE WHEN sqlc.arg('set_entry_machine_hours')::bool
                               THEN sqlc.narg('entry_machine_hours')::float8
                               ELSE entry_machine_hours END,
    entry_rung = CASE WHEN sqlc.arg('set_entry_rung')::bool
                      THEN sqlc.narg('entry_rung') ELSE entry_rung END,
    updated_at = now()
WHERE slug = sqlc.arg('slug')
RETURNING id, slug, name, logo_url, starting_price::float8 AS starting_price,
          shopify_url, description, is_active, ladder,
          cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
          entry_machine_hours, entry_rung, created_at, updated_at;
