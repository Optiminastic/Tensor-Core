-- What a colour name means to the printers. See migration 0074 for why this is
-- its own table rather than a column on filament_inventory.
--
-- Names are matched lower(trim(...)) on both sides throughout: the caller passes
-- production.CanonicalColourName's uppercase output, and a row typed in by hand
-- should still match.

-- name: GetColourMapPrimaryHex :one
-- The swatch to render this colour as, and the one the queue dialog shows.
SELECT hex FROM colour_map
WHERE lower(trim(colour_name)) = lower(trim(sqlc.arg('colour_name')::text))
  AND is_primary
LIMIT 1;

-- name: ListColourMap :many
SELECT * FROM colour_map
ORDER BY lower(trim(colour_name)), is_primary DESC, hex;

-- name: UpsertColourMapEntry :one
-- Records "this hex is our BLUE". Re-confirming an existing pair refreshes who
-- said so and when, rather than failing.
INSERT INTO colour_map (id, colour_name, hex, is_primary, note, confirmed_by, confirmed_at)
VALUES (
    sqlc.arg('id'), sqlc.arg('colour_name'), upper(sqlc.arg('hex')::text),
    sqlc.arg('is_primary'), sqlc.narg('note'), sqlc.narg('confirmed_by'), now()
)
ON CONFLICT (lower(trim(colour_name)), upper(hex)) DO UPDATE SET
    note         = COALESCE(EXCLUDED.note, colour_map.note),
    confirmed_by = EXCLUDED.confirmed_by,
    confirmed_at = now(),
    updated_at   = now()
RETURNING *;

-- name: UpdateColourMapEntry :one
-- Corrects a recorded spool: its value, its note, or which colour it belongs to.
--
-- colour_name moves the spool to another colour. is_primary is deliberately not
-- settable here - a single UPDATE cannot demote the colour's previous primary
-- in the same breath, and two primaries violate the partial unique index. The
-- handler promotes through SetColourMapPrimary instead.
UPDATE colour_map SET
    colour_name  = COALESCE(sqlc.narg('colour_name'), colour_name),
    hex          = COALESCE(upper(sqlc.narg('hex')::text), hex),
    note         = COALESCE(sqlc.narg('note'), note),
    confirmed_by = COALESCE(sqlc.narg('confirmed_by'), confirmed_by),
    confirmed_at = now(),
    updated_at   = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: SetColourMapPrimary :exec
-- Makes one row the primary for its colour, in ONE statement.
--
-- Deliberately not "clear the old, set the new": between two statements the
-- colour would have no primary, or briefly two, and uq_colour_map_primary would
-- reject the second write. Setting every row of the name in a single UPDATE
-- moves the flag atomically.
UPDATE colour_map SET
    is_primary = (colour_map.id = sqlc.arg('id')),
    updated_at = now()
WHERE lower(trim(colour_map.colour_name)) = (
    SELECT lower(trim(cm.colour_name)) FROM colour_map cm WHERE cm.id = sqlc.arg('id')
);

-- name: DeleteColourMapEntry :execrows
DELETE FROM colour_map WHERE id = sqlc.arg('id');

-- name: CountColourMapNames :one
-- How many colours the map names - the Colour map table pages by colour, so
-- a colour's alternatives never split across pages.
SELECT count(DISTINCT colour_name) FROM colour_map;

-- name: ListColourMapTable :many
-- One page of colours (LIMIT/OFFSET over names), with every entry under them.
WITH names AS (
    SELECT DISTINCT colour_name FROM colour_map
    ORDER BY colour_name
    LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset')
)
SELECT cm.* FROM colour_map cm
JOIN names n ON n.colour_name = cm.colour_name
ORDER BY cm.colour_name, cm.is_primary DESC, cm.hex;
