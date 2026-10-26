-- +goose Up
-- How many characters a product's own design fits in one field.
--
-- There was one number for every product, twelve, and it was the plank's:
-- personalise.maxNameLetters, the longest name a Dual Name Plank lays out
-- before the letters run off the end. Every registry product borrowed it
-- because it was the tightest, which was true while the only registry
-- products were small plates.
--
-- The lotus holder's text box is 100 x 30 mm and wraps onto two lines, so it
-- takes about sixteen. Raising the shared constant would have let a plank
-- take sixteen too and fail thirty seconds later in a render - which is the
-- exact failure the constant exists to prevent.
--
-- NULL means "the default", so every field configured before this keeps the
-- twelve it already had. A product that knows better says so here, and the
-- person who changes the .scad's box size is the person who can change this
-- beside it, without a deploy.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS max_runes integer;

ALTER TABLE product_field_maps
    DROP CONSTRAINT IF EXISTS ck_field_map_max_runes;

-- An upper bound as well as a lower one: a box that claims to fit 500
-- characters is a typo, and the render that proves it costs a minute.
ALTER TABLE product_field_maps
    ADD CONSTRAINT ck_field_map_max_runes CHECK (
        max_runes IS NULL OR (max_runes > 0 AND max_runes <= 120)
    );

-- +goose Down
ALTER TABLE product_field_maps DROP CONSTRAINT IF EXISTS ck_field_map_max_runes;
ALTER TABLE product_field_maps DROP COLUMN IF EXISTS max_runes;
