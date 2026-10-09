-- +goose Up
-- The coloured pieces one design file prints as.
--
-- Colour has never been configurable. The renderer runs every template twice -
-- PART="base" and PART="text" - paints the first white and the second whatever
-- the customer chose, and assembles the two into a 3MF. That is a plank
-- described in Go: exactly two pieces, the fixed one always white, and no way
-- to say that a rose's heart is red while its stem follows the order.
--
-- A row here is one piece of one product's design file. part_name is BOTH the
-- value passed as -D PART= and the object name in the reference 3MF the parts
-- were read from, deliberately: they have to agree for the render to produce
-- the piece at all, so storing them as one value removes the chance of a
-- mapping between them drifting.
--
-- colour_hex NULL means "whatever the customer chose", and that is why it is a
-- nullable colour rather than a colour plus a boolean: two columns can disagree
-- - fixed with no hex, or a hex that is ignored - and there is no state here
-- that needs them to.
--
-- Products with no rows keep the two-pass behaviour exactly. This is additive
-- in the same way the registry render path is: nothing printing today changes
-- until somebody configures it.
CREATE TABLE IF NOT EXISTS design_colour_parts (
    id         uuid PRIMARY KEY,
    product_id uuid        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    role       varchar(24) NOT NULL DEFAULT 'body',
    part_name  varchar(64) NOT NULL,
    colour_hex varchar(7),
    position   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Rejected at the boundary AND here: a colour that is not #RRGGBB reaches
    -- Write3MF, which refuses it, and a job fails at render time for a typo
    -- made in a form weeks earlier.
    CONSTRAINT ck_design_colour_part_hex CHECK (
        colour_hex IS NULL OR colour_hex ~ '^#[0-9A-Fa-f]{6}$'
    )
);

-- One row per piece per role. Case-insensitive because OpenSCAD string
-- comparison is not, and "Base" and "base" being two rows would render one
-- piece twice and the other never.
CREATE UNIQUE INDEX IF NOT EXISTS uq_design_colour_part
    ON design_colour_parts (product_id, lower(role), lower(part_name));

CREATE INDEX IF NOT EXISTS ix_design_colour_parts_product
    ON design_colour_parts (product_id, role);

-- +goose Down
DROP TABLE IF EXISTS design_colour_parts;
