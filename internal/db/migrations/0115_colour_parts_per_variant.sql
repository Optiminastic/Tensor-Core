-- +goose Up
-- A product's coloured pieces may differ PER VARIANT.
--
-- design_colour_parts was keyed on (product, role, part), which can say two
-- things: this piece is always #D4AF37, or this piece follows whatever colour
-- the customer bought. Both are properties of the PRODUCT, and that covers the
-- plank - a white base and the customer's colour on the lettering - because
-- only one piece there ever moves.
--
-- It does not cover a product whose whole palette is designed per colourway.
-- The Personalized Wall Diya Holder is three pieces and the shop has chosen a
-- scheme for each variant:
--
--     Purple : back purple, outline PINK,   text GOLD
--     Red    : back red,    outline GOLD,   text YELLOW
--     White  : back white,  outline GOLD,   text RED
--
-- The back follows the customer in all three, so that piece was already
-- expressible. The outline and the text are not: they change with the variant
-- while being nothing like the colour the customer picked. There was no way to
-- write that down, so the product could not be configured at all.
--
-- variant_id NULL keeps its old meaning exactly - the product's scheme, used by
-- every variant that has not declared one of its own - so every row that exists
-- today goes on meaning what it meant.
ALTER TABLE design_colour_parts
    ADD COLUMN IF NOT EXISTS variant_id uuid REFERENCES product_variants (id) ON DELETE CASCADE;

-- One unique index becomes two, because a NULL variant_id does not collide in
-- a plain unique index and the product-level row would stop being unique the
-- moment a variant declared the same piece.
DROP INDEX IF EXISTS uq_design_colour_part;

CREATE UNIQUE INDEX IF NOT EXISTS uq_design_colour_part_product
    ON design_colour_parts (product_id, lower(role), lower(part_name))
    WHERE variant_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_design_colour_part_variant
    ON design_colour_parts (variant_id, lower(role), lower(part_name))
    WHERE variant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_design_colour_parts_variant
    ON design_colour_parts (variant_id, role)
    WHERE variant_id IS NOT NULL;

-- +goose Down
-- The variant rows go, not just the column: they are the override, and leaving
-- them behind as product-level rows would make every variant of a product
-- print one arbitrary variant's scheme.
DELETE FROM design_colour_parts WHERE variant_id IS NOT NULL;

DROP INDEX IF EXISTS ix_design_colour_parts_variant;
DROP INDEX IF EXISTS uq_design_colour_part_variant;
DROP INDEX IF EXISTS uq_design_colour_part_product;

ALTER TABLE design_colour_parts
    DROP COLUMN IF EXISTS variant_id;

CREATE UNIQUE INDEX IF NOT EXISTS uq_design_colour_part
    ON design_colour_parts (product_id, lower(role), lower(part_name));
