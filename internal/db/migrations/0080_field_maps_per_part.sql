-- +goose Up
-- A product may print from more than one .scad, and not every mapped field is
-- required.
--
-- 0079 assumed one template per product, because every product then had one.
-- A Soulmate Combo is three printed things sold as one line - a plank, a rose
-- and a keychain - and each has its own file, its own variables and its own
-- name from the customer. JOB-115257 carried "Name On 3D Rose" = "AJ" and
-- "Name On Heart Keychain" = "AJ", rendered neither, and nobody could tell:
-- the fields were stored and silently never read.
--
-- variant_designs.role already holds any number of named designs per variant
-- and always did - it is a free varchar with a unique index on
-- (variant_id, role) WHERE active, and 'body'/'base' was only ever an enum in
-- the frontend. The mapping is what could not follow it here.

-- Which design file this row feeds. Matches variant_designs.role.
--
-- Defaulted to 'body', which is what every existing row already is, so this
-- migration changes no behaviour for any product configured today.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS role varchar(24) NOT NULL DEFAULT 'body';

-- Whether a missing answer holds the job.
--
-- Required is right for a plank: a blank name is scrap, and holding the job
-- naming the field is how somebody finds out. It is wrong for a rose whose
-- name the customer left empty - two of the seven combos in the database
-- carry no rose name at all, and under a required-everything rule those two
-- orders would sit held from the day this shipped.
--
-- Optional and absent passes no -D for the variable, so the template's own
-- default stands. It does NOT pass an empty string, which would engrave "" on
-- a rose rather than leaving it plain.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS required boolean NOT NULL DEFAULT true;

-- One row per variable PER DESIGN FILE. Two rows naming NAME_L for the same
-- file would both become -D flags and OpenSCAD takes the last on the command
-- line, so the model would depend on iteration order. Across two files they
-- are different variables in different scripts: the rose and the keychain may
-- both declare TEXT, and refusing that would mean renaming a variable inside
-- somebody's .scad to satisfy Tensor.
DROP INDEX IF EXISTS uq_product_field_map_variable;
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_field_map_variable
    ON product_field_maps (product_id, role, lower(scad_variable));

-- The render path asks for one product's mapping for ONE file, once per model
-- generated. role joins the index for that reason.
DROP INDEX IF EXISTS ix_product_field_maps_product;
CREATE INDEX IF NOT EXISTS ix_product_field_maps_product
    ON product_field_maps (product_id, role, position);

-- +goose Down
DROP INDEX IF EXISTS ix_product_field_maps_product;
CREATE INDEX IF NOT EXISTS ix_product_field_maps_product
    ON product_field_maps (product_id, position);

DROP INDEX IF EXISTS uq_product_field_map_variable;
-- Recreating the old index can fail where it should: a product that has been
-- given a second design file has two rows for one variable, which is correct
-- now and was impossible before. Rolling back past that needs the extra rows
-- deleted first, and doing it silently here would throw away a mapping.
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_field_map_variable
    ON product_field_maps (product_id, lower(scad_variable));

ALTER TABLE product_field_maps DROP COLUMN IF EXISTS required;
ALTER TABLE product_field_maps DROP COLUMN IF EXISTS role;
