-- +goose Up
-- Which order field feeds which OpenSCAD variable, per product.
--
-- Tensor knows "STEP 4-First Name-" means NAME_L because ParamsFromProperties
-- says so in Go (personalise/dnp.go). That is fine for one product family and
-- impossible for a second: adding a keychain means a developer, a recompile
-- and a deploy, which is the same wall generatedSKUSegments put us behind
-- twice in one day.
--
-- This is that knowledge as data. With a template on the variant and a mapping
-- here, a product renders without a code change.
--
-- PRODUCT-level, not variant-level. NAME_L is the same for every colour, so a
-- per-variant mapping would be the same twenty rows twenty times, and twenty
-- places for them to disagree. The SKU is how an order FINDS the product -
-- product_variants.sku already carries it - and the mapping belongs to the
-- product it describes.
CREATE TABLE IF NOT EXISTS product_field_maps (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,

    -- The customer's field, stored NORMALISED - lower case, punctuation
    -- collapsed to single spaces, exactly as normalisePropKey in
    -- shopify_import.go produces. "STEP 4-First Name-:" is written here as
    -- "step 4 first name".
    --
    -- Normalised rather than verbatim because the storefront writes the same
    -- field three ways across three products ("STEP 4-First Name-", "STEP 2 -
    -- First Name-", "First Name on Plank"), and a mapping that had to match
    -- the punctuation would break the first time somebody edited a label.
    property_key varchar(120) NOT NULL,

    -- The variable in the .scad. Must be one the template declares, which
    -- personalise.DeclaresAll checks: OpenSCAD accepts an unknown -D without
    -- complaint and simply never reads it, so a typo here renders a model
    -- missing the thing the customer asked for, successfully.
    scad_variable varchar(64) NOT NULL,

    -- 'string' or 'number', and it decides quoting. This is not cosmetic: an
    -- unquoted string is an OpenSCAD syntax error, and a quoted number is a
    -- string that silently fails every arithmetic comparison the script makes
    -- against it - no error, just the wrong shape.
    value_type varchar(16) NOT NULL DEFAULT 'string',

    -- Presentation order in the editor. The mapping is read as a list by
    -- whoever maintains it, and "first name, second name, hearts" is easier to
    -- check than whatever order the rows were inserted in.
    position   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_field_maps_value_type_check
        CHECK (value_type IN ('string', 'number'))
);

-- One row per variable per product, enforced rather than trusted. Two rows
-- naming NAME_L would both become -D flags and OpenSCAD takes the last one on
-- the command line - so the model would depend on map iteration order, which
-- is to say on nothing at all.
--
-- lower(), because the editor is typed by a person and NAME_L and name_l are
-- the same variable to OpenSCAD.
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_field_map_variable
    ON product_field_maps (product_id, lower(scad_variable));

-- The render path's only question: "what does this product map?". Answered
-- once per model generation, so it is worth an index rather than a scan.
CREATE INDEX IF NOT EXISTS ix_product_field_maps_product
    ON product_field_maps (product_id, position);

-- +goose Down
DROP INDEX IF EXISTS ix_product_field_maps_product;
DROP INDEX IF EXISTS uq_product_field_map_variable;
DROP TABLE IF EXISTS product_field_maps;
