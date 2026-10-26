-- +goose Up
-- What colour a printed component appears in, when the customer is shown the
-- assembled product.
--
-- The preview has only ever shown what TENSOR renders - the plate and the
-- lettering - because that is the only part it builds. The customer is looking
-- at a diya holder with no holder: the foot and the cup are printed from their
-- own files and attached at the bench, so they never reached the picture.
--
-- NULL means the piece takes the colour the customer chose, which is what the
-- foot does. A hex pins it, which is what the cup does: gold on every
-- colourway, the same on all three.
--
-- It describes the PICTURE, not the print. The model on disk stays the file
-- the floor slices; these pieces are composed on the way out, exactly as the
-- plank's hearts already are.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS preview_colour varchar(7);

ALTER TABLE inventory_items
    DROP CONSTRAINT IF EXISTS ck_inventory_item_preview_colour;

ALTER TABLE inventory_items
    ADD CONSTRAINT ck_inventory_item_preview_colour CHECK (
        preview_colour IS NULL OR preview_colour ~ '^#[0-9A-Fa-f]{6}$'
    );

-- The order pieces are drawn in, so a foot sits under a cup rather than
-- whichever the database happened to return first.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS preview_position integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE inventory_items DROP CONSTRAINT IF EXISTS ck_inventory_item_preview_colour;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS preview_position;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS preview_colour;
