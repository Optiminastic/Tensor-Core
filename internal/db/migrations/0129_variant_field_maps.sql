-- +goose Up
-- A field mapping that belongs to ONE colourway.
--
-- A mapping says "the customer's Your name feeds NAME", and until now it said
-- it for a whole product. That is right while every colourway of a product
-- prints from one file: three variants of one drawing declare one variable.
--
-- It stops being right the moment each child SKU carries its own .scad, which
-- is what launching a product now means. Those files are separate drawings by
-- separate hands, and an assembled Royal holder may call the customer's text
-- NAME where the plate calls it TEXT. Forcing one name across them refuses a
-- correct file over a spelling.
--
-- NULL is the product's mapping, which every existing row is and stays. A row
-- with a variant_id overrides it for that colourway alone.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS variant_id uuid REFERENCES product_variants (id) ON DELETE CASCADE;

-- The uniqueness rule splits in two, because the old one named product_id and
-- a variant's rows would collide with the product's on the same variable.
--
-- BOTH ARE PARTIAL, and that is load-bearing: an upsert's ON CONFLICT has to
-- name a partial index's WHERE clause as well as its columns, or Postgres
-- refuses with 42P10 and cannot tell you why. See UpsertSKUPipeline, which
-- learned this the same way.
DROP INDEX IF EXISTS uq_product_field_map_variable;

CREATE UNIQUE INDEX IF NOT EXISTS uq_product_field_map_variable
    ON product_field_maps (product_id, role, lower(scad_variable))
    WHERE variant_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_variant_field_map_variable
    ON product_field_maps (variant_id, role, lower(scad_variable))
    WHERE variant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_product_field_maps_variant
    ON product_field_maps (variant_id, role, position)
    WHERE variant_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_product_field_maps_variant;
DROP INDEX IF EXISTS uq_variant_field_map_variable;
DROP INDEX IF EXISTS uq_product_field_map_variable;
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_field_map_variable
    ON product_field_maps (product_id, role, lower(scad_variable));
ALTER TABLE product_field_maps DROP COLUMN IF EXISTS variant_id;
