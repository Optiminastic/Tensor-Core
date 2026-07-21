-- Brands: identity plus the pricing policy the engine reads. numeric columns are
-- cast to/from float8; the key enum via text; ladder stays json ([]byte in Go).

-- name: ListBrands :many
SELECT id, key::text AS key, name, starting_price::float8 AS starting_price,
       shopify_url, description, is_active, ladder,
       cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
       entry_machine_hours, entry_rung,
       created_at, updated_at
FROM brands ORDER BY key;

-- name: GetBrandByKey :one
SELECT id, key::text AS key, name, starting_price::float8 AS starting_price,
       shopify_url, description, is_active, ladder,
       cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
       entry_machine_hours, entry_rung,
       created_at, updated_at
FROM brands WHERE key = $1::brand;

-- name: BrandExists :one
SELECT EXISTS (SELECT 1 FROM brands WHERE key = $1::brand) AS brand_exists;

-- name: InsertBrand :one
INSERT INTO brands (
    id, key, name, starting_price, shopify_url, description, is_active, ladder,
    cp_green_max, cp_yellow_max, entry_machine_hours, entry_rung
) VALUES (
    sqlc.arg('id'), sqlc.arg('key')::brand, sqlc.arg('name'), sqlc.arg('starting_price')::float8,
    sqlc.narg('shopify_url'), sqlc.narg('description'), sqlc.arg('is_active'), sqlc.arg('ladder')::json,
    sqlc.arg('cp_green_max')::float8, sqlc.arg('cp_yellow_max')::float8,
    sqlc.narg('entry_machine_hours')::float8, sqlc.narg('entry_rung')
)
RETURNING id, key::text AS key, name, starting_price::float8 AS starting_price,
          shopify_url, description, is_active, ladder,
          cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
          entry_machine_hours, entry_rung,
          created_at, updated_at;

-- name: UpdateBrand :one
UPDATE brands SET
    name = COALESCE(sqlc.narg('name'), name),
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
WHERE key = sqlc.arg('key')::brand
RETURNING id, key::text AS key, name, starting_price::float8 AS starting_price,
          shopify_url, description, is_active, ladder,
          cp_green_max::float8 AS cp_green_max, cp_yellow_max::float8 AS cp_yellow_max,
          entry_machine_hours, entry_rung,
          created_at, updated_at;
