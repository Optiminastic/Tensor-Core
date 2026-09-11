-- +goose Up
-- A stable handle for a part, so a bill of materials can point at it.
--
-- inventory_items has been keyed on the case-insensitive NAME since 0067, which
-- is right for a shelf somebody reads down looking for "mailers". It is wrong
-- for a reference: a BOM line saying "this product needs one LED-10CM" must
-- survive the day the shelf is renamed to "LED strip (10cm, warm white)", and a
-- name-keyed reference cannot.
--
-- Nullable, and no back-fill. Every row on the shelf today was entered to be
-- counted, not to be referenced, and inventing codes for them would put
-- guessed data in front of the person who has to trust it. A code is required
-- only when a part is first used in a BOM - the Registry page asks for one
-- there, where the reason for it is obvious.
--
-- Unique case-insensitively, matching how the name is already treated: "LED-001"
-- and "led-001" are one part, and letting both exist would reintroduce exactly
-- the split shelf uq_inventory_item_name was added to prevent.
ALTER TABLE inventory_items ADD COLUMN IF NOT EXISTS code varchar(64);

CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_item_code
    ON inventory_items (lower(code)) WHERE code IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_inventory_item_code;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS code;
