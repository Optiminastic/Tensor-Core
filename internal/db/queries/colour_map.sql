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

-- name: ListColourMapHexesByName :many
-- Every hex this colour may legitimately appear as in an AMS, primary first.
--
-- This is what the slot-to-tray assignment matches against: thirteen printers do
-- not agree on blue, so a bed needing BLUE is satisfied by any spool the shop
-- has confirmed IS blue - and by nothing else.
SELECT hex FROM colour_map
WHERE lower(trim(colour_name)) = lower(trim(sqlc.arg('colour_name')::text))
ORDER BY is_primary DESC, hex;

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

-- name: CountColourMapEntries :one
-- How many colours the shop has confirmed. The colour map labels slots and
-- suggests machines; nothing depends on it to send, since the operator binds
-- slots to trays explicitly.
SELECT count(*) FROM colour_map;
