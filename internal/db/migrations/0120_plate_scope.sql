-- +goose Up
-- Whether a component's plate is the same for every SKU, or one per SKU.
--
-- 0119 made every plate per SKU, which is right for a part whose colour
-- follows the variant - the Royal's diya cup is purple on the purple one - and
-- badly wrong for a part that does not. The Dual Name Plank's red heart is the
-- same red heart on all twenty variants, so per SKU it asks for sixty files
-- where three would do, and fifty-seven of them would be copies.
--
-- 'shared' plates are stored with sku = '', which the column already allows
-- and the unique index already keys on - so one set per machine class falls
-- out of the same table rather than needing another.
--
-- DEFAULT 'per_sku' because that is what 0119 created and what the rows
-- written under it mean. A component that needs the other answer says so.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS plate_scope varchar(16) NOT NULL DEFAULT 'per_sku';

ALTER TABLE inventory_items
    DROP CONSTRAINT IF EXISTS ck_inventory_item_plate_scope;

ALTER TABLE inventory_items
    ADD CONSTRAINT ck_inventory_item_plate_scope
        CHECK (plate_scope IN ('shared', 'per_sku'));

-- +goose Down
ALTER TABLE inventory_items DROP CONSTRAINT IF EXISTS ck_inventory_item_plate_scope;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS plate_scope;
