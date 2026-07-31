-- Filament inventory. grams_available and reorder_level_grams are NOT NULL, so
-- they are selected raw (pgtype.Numeric) and mapped with db.NumFloat. Lookups key
-- on (material, COALESCE(colour, '')) so an untinted material is a single bucket.

-- name: GetFilamentByMaterialColour :one
SELECT * FROM filament_inventory
WHERE material = sqlc.arg('material')
  AND COALESCE(colour, '') = COALESCE(sqlc.narg('colour'), '');

-- name: InsertFilament :one
INSERT INTO filament_inventory (id, material, colour, grams_available, reorder_level_grams)
VALUES (
    sqlc.arg('id'), sqlc.arg('material'), sqlc.narg('colour'),
    sqlc.arg('grams_available')::float8, sqlc.arg('reorder_level_grams')::float8
)
RETURNING *;

-- name: UpdateFilamentLevel :one
UPDATE filament_inventory SET
    grams_available     = sqlc.arg('grams_available')::float8,
    reorder_level_grams = sqlc.arg('reorder_level_grams')::float8,
    updated_at          = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: AdjustFilamentStock :exec
-- Applies a signed delta to available grams. No clamp: stock may go negative when a
-- failure wasted more than was on hand.
UPDATE filament_inventory SET
    grams_available = grams_available + sqlc.arg('delta')::float8,
    updated_at      = now()
WHERE material = sqlc.arg('material')
  AND COALESCE(colour, '') = COALESCE(sqlc.narg('colour'), '');

-- name: ListFilament :many
SELECT * FROM filament_inventory ORDER BY material, colour NULLS FIRST;

-- name: ListFilamentPage :many
SELECT * FROM filament_inventory
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');
