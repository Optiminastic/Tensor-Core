-- +goose Up
-- The things a product prints, named and configured before any file exists.
--
-- A part USED TO BE DERIVED: it existed because a file had been assigned to
-- it, and the stated reason was that a second list is a second thing to keep
-- in step with the first. That holds right up until somebody configures a
-- product the way the shop actually does it - name the three things a combo
-- prints, then go and find their files. Derived, there was nowhere to put the
-- name until a file arrived, so the first file of eight had to be given in a
-- different place and a different shape from the other seven.
--
-- It also had nowhere to record the one decision that shapes the whole panel:
-- whether this part prints ONE file for every colourway or a file per child
-- SKU. Without it the page had to offer both at once, so every part showed a
-- product-wide picker AND a row per SKU and nothing said which one was meant
-- to win.
--
-- Kept ADDITIVE on purpose. listProductParts still derives parts from the
-- files, and merges these over the top, so every product configured before
-- this table existed reads exactly as it did and nothing needs backfilling.
CREATE TABLE IF NOT EXISTS product_parts (
    id          uuid PRIMARY KEY,
    product_id  uuid NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    -- The operator's word for it - 'plank', 'rose', 'keychain'. The same
    -- string that keys variant_designs.role, product_field_maps.role,
    -- design_colour_parts.role and production_jobs.part_role, which is why
    -- renaming one has to carry all four with it.
    role        varchar(64) NOT NULL,
    -- true: a file per child SKU. false: one file for every colourway.
    --
    -- Defaults to true because that is the case that needs the registry. A
    -- product whose colourways are one geometry can say so in one click; a
    -- product whose colourways are different drawings cannot be expressed at
    -- all by the other default.
    per_variant boolean NOT NULL DEFAULT true,
    position    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One part per name per product, case-insensitively: 'Rose' and 'rose' are
-- the same thing to every table that joins on this string.
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_parts_role
    ON product_parts (product_id, lower(role));

-- +goose Down
DROP TABLE IF EXISTS product_parts;
