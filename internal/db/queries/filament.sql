-- Filament inventory. grams_available and reorder_level_grams are NOT NULL, so
-- they are selected raw (pgtype.Numeric) and mapped with db.NumFloat. Lookups key
-- on (material, COALESCE(colour, '')) so an untinted material is a single bucket.

-- name: GetFilamentByMaterialColour :one
SELECT * FROM filament_inventory
WHERE material = sqlc.arg('material')
  AND COALESCE(colour, '') = COALESCE(sqlc.narg('colour'), '');

-- name: InsertFilament :one
INSERT INTO filament_inventory (id, material, colour, colour_hex, grams_available, reorder_level_grams)
VALUES (
    sqlc.arg('id'), sqlc.arg('material'), sqlc.narg('colour'), sqlc.narg('colour_hex'),
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

-- name: SetFilamentStockFromSource :one
-- The sync's write: stock and swatch from BambuBuddy, reorder level untouched.
-- Separate from UpdateFilamentLevel because that one is the operator's edit
-- form, where the reorder level IS the thing being changed - folding the two
-- together would let a sync silently reset a threshold someone chose.
UPDATE filament_inventory SET
    grams_available = sqlc.arg('grams_available')::float8,
    colour_hex      = COALESCE(sqlc.narg('colour_hex'), colour_hex),
    updated_at      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteFilamentNotIn :exec
-- Removes rows the source no longer reports. Only ever called from the
-- operator-initiated sync: a shelf that lost a material is a real change, but
-- it is not one a background job should act on unasked.
-- Keys are material and colour joined by chr(31), the ASCII unit separator -
-- a single array is far easier for sqlc to reason about than a two-column
-- unnest, and chr(31) cannot occur in a material or colour name.
DELETE FROM filament_inventory
WHERE (material || chr(31) || COALESCE(colour, '')) <> ALL (sqlc.arg('keys')::text[]);

-- name: ListFilamentKeys :many
SELECT material, COALESCE(colour, '') AS colour FROM filament_inventory;

-- name: GetColourHexByName :one
-- The swatch BambuBuddy holds for a colour name, matched case-insensitively.
--
-- The filament shelf is the best source for this: it is the colour that will
-- physically be loaded, kept current by the sync, and it already carries the
-- hex BambuBuddy shows. Any row of that colour will do - the hex is a property
-- of the colour, not of the material.
SELECT colour_hex FROM filament_inventory
WHERE colour_hex IS NOT NULL
  AND lower(trim(colour)) = lower(trim(sqlc.arg('colour')::text))
LIMIT 1;
