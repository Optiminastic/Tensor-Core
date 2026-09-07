-- name: ListInventoryItems :many
-- Everything on the shelf that is not filament, alphabetically.
--
-- Alphabetical rather than newest-first: this is a list somebody reads down
-- looking for a specific item ("have we got mailers?"), not a feed of what
-- changed. The Filament tab beside it sorts by material for the same reason.
SELECT * FROM inventory_items
ORDER BY lower(name), id;

-- name: UpsertInventoryItem :one
-- Records an item, or restocks one already on the shelf.
--
-- Keyed on the case-insensitive name, matching uq_inventory_item_name, so
-- adding "Gift box" when "Gift Box" exists updates that row rather than failing
-- on the index or opening a second shelf for the same thing.
--
-- The incoming quantity REPLACES what was there. Adding to it was considered
-- and rejected: the dialog asks "how many do you have", and a stock count that
-- silently doubled because somebody submitted twice is worse than one that has
-- to be typed correctly. The name and price are overwritten for the same
-- reason - the latest entry is the current truth.
-- The ::float8 casts match filament.sql: they make sqlc emit float64 rather
-- than pgtype.Numeric, so a handler passes a plain number.
INSERT INTO inventory_items (id, name, quantity, unit, unit_price)
VALUES (
    sqlc.arg('id'), sqlc.arg('name'), sqlc.arg('quantity')::float8,
    sqlc.arg('unit'), sqlc.narg('unit_price')::float8
)
ON CONFLICT (lower(name)) DO UPDATE
SET name       = EXCLUDED.name,
    quantity   = EXCLUDED.quantity,
    unit       = EXCLUDED.unit,
    unit_price = EXCLUDED.unit_price,
    updated_at = now()
RETURNING *;

-- name: DeleteInventoryItem :execrows
-- Removes an item from the shelf entirely.
--
-- A hard delete, unlike a job or a bed: this row is a stock count, not a record
-- of work done, so there is no history in it worth keeping once the shop stops
-- carrying the item.
DELETE FROM inventory_items WHERE id = @id;
