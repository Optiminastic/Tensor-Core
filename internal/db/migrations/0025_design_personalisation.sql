-- The applied personalisation on a specific design: the customer's name rendered
-- as real geometry, with its placement (offset, rotation), size, depth, font and
-- an intended colour. Distinct from personalisation_rules (what a product OFFERS).
-- personalised_stl_key points at the baked (base + text) STL that the slicer then
-- uses, so cost and G-code include the text. NULL personalisation = plain design.

-- +goose Up
ALTER TABLE designs ADD COLUMN IF NOT EXISTS personalisation jsonb;
ALTER TABLE designs ADD COLUMN IF NOT EXISTS personalised_stl_key text;

-- +goose Down
ALTER TABLE designs DROP COLUMN IF EXISTS personalised_stl_key;
ALTER TABLE designs DROP COLUMN IF EXISTS personalisation;
