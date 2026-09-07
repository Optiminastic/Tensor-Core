-- +goose Up
-- Everything on the shelf that is not filament.
--
-- The Inventory page has only ever tracked spools, because filament is what the
-- planner reserves and what a print physically consumes. But a plank does not
-- ship as a plank: it ships in a box, with an insert, a card and tape, and when
-- the boxes run out the floor stops just as hard as when the PLA does. None of
-- that had anywhere to live.
--
-- A separate table rather than more columns on filament_inventory. That table is
-- keyed (material, colour) and its grams are read by the batch planner, the
-- colour resolver and the reservation path; widening it so a row could mean "40
-- gift boxes" would put a unit-less quantity in front of every one of them.
--
-- Deliberately plain: a name, how many, of what, and what one costs. No reorder
-- level, no supplier, no location - those are guesses about a workflow nobody
-- has described yet, and an empty column invites being filled with noise.
CREATE TABLE IF NOT EXISTS inventory_items (
    id         uuid PRIMARY KEY,
    name       varchar(255) NOT NULL,
    -- Fractional because a unit may be a kilogram: "0.5 kg of infill beads" is
    -- a real line, and rounding it to zero would read as out of stock.
    quantity   numeric(12, 3) NOT NULL DEFAULT 0,
    -- kg, unit, piece, box... validated at the HTTP boundary against a known
    -- set rather than by a CHECK, so adding one is a code change reviewed
    -- alongside the UI that offers it, not a migration.
    unit       varchar(32) NOT NULL,
    -- Per ONE unit, not for the quantity held. Null means nobody has recorded
    -- it, which is different from free - a zero here would understate the cost
    -- of a plank that carries the item.
    unit_price numeric(12, 2),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per item, case-insensitively: "Gift Box" and "gift box" are the same
-- shelf, and two rows for it would let one be restocked while the other reads
-- empty.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_item_name
    ON inventory_items (lower(name));

-- +goose Down
DROP INDEX IF EXISTS uq_inventory_item_name;
DROP TABLE IF EXISTS inventory_items;
