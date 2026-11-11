-- Custom columns on the inventory shelves (0141). A row's values live in its
-- own `extra`, keyed by the column's name.

-- name: ListInventoryCustomColumns :many
SELECT * FROM inventory_custom_columns
WHERE shelf = sqlc.arg('shelf')
ORDER BY position, created_at;

-- name: EnsureInventoryCustomColumn :one
-- Adds a column, or returns the one already there under that name in any
-- case - an import naming "size" when "Size" exists reuses it.
INSERT INTO inventory_custom_columns (id, shelf, name, position)
VALUES (
    sqlc.arg('id'), sqlc.arg('shelf')::varchar, sqlc.arg('name'),
    COALESCE((SELECT max(position) + 1 FROM inventory_custom_columns WHERE shelf = sqlc.arg('shelf')::varchar), 0)
)
ON CONFLICT (shelf, lower(name)) DO UPDATE SET name = inventory_custom_columns.name
RETURNING *;

-- name: DeleteInventoryCustomColumn :one
DELETE FROM inventory_custom_columns WHERE id = sqlc.arg('id')
RETURNING *;

-- name: RemoveFilamentExtraKey :exec
UPDATE filament_inventory SET extra = extra - sqlc.arg('key')::text;

-- name: RemoveItemExtraKey :exec
UPDATE inventory_items SET extra = extra - sqlc.arg('key')::text;

-- name: MergeFilamentExtra :exec
-- Overlays values onto a row; keys not named keep theirs.
UPDATE filament_inventory SET extra = extra || sqlc.arg('extra')::jsonb, updated_at = now()
WHERE id = sqlc.arg('id');

-- name: MergeItemExtra :exec
UPDATE inventory_items SET extra = extra || sqlc.arg('extra')::jsonb, updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ClearFilamentExtraValue :exec
UPDATE filament_inventory SET extra = extra - sqlc.arg('key')::text, updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ClearItemExtraValue :exec
UPDATE inventory_items SET extra = extra - sqlc.arg('key')::text, updated_at = now()
WHERE id = sqlc.arg('id');

-- name: SetFilamentColourHex :exec
-- The swatch an import found for a row. overwrite is true when the sheet
-- itself gave the hex; a hex worked out from the name only fills a gap.
UPDATE filament_inventory
SET colour_hex = CASE WHEN sqlc.arg('overwrite')::bool THEN sqlc.arg('hex')::text
                      ELSE COALESCE(colour_hex, sqlc.arg('hex')::text) END
WHERE id = sqlc.arg('id');

-- name: UpdateFilamentRow :one
-- The Edit action on a filament row: every field, addressed by id, so the
-- material or colour can be corrected - the upsert is keyed on them and can't.
UPDATE filament_inventory
SET material            = sqlc.arg('material'),
    colour              = sqlc.narg('colour'),
    colour_hex          = sqlc.narg('colour_hex'),
    grams_available     = sqlc.arg('grams_available')::float8,
    reorder_level_grams = sqlc.arg('reorder_level_grams')::float8,
    updated_at          = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteFilamentRow :one
DELETE FROM filament_inventory WHERE id = sqlc.arg('id')
RETURNING id;

-- name: FillJobColoursFromModel :exec
-- A rendered model's own colours, recorded on a job that names none - a
-- product whose .scad declares its colours, ordered with no colour at all.
-- Fills a gap only: a colour the order or an operator stated is never replaced.
UPDATE production_jobs
SET colours = sqlc.arg('colours')::jsonb, updated_at = now()
WHERE id = sqlc.arg('id')
  AND colours = '[]'::jsonb
  AND (colour IS NULL OR btrim(colour) = '');
