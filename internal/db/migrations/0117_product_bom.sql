-- +goose Up
-- A bill of materials belongs to the PRODUCT, and a SKU may override it.
--
-- variant_bom is keyed on the variant, which is right for a part only one
-- colourway carries and wrong for every part they all do. A red heart goes on
-- every Dual Name Plank; declared per variant that is forty-four rows saying
-- the same thing, and forty-four chances for one of them to drift.
--
-- NULL product rows are the default every variant inherits. A variant that
-- declares a bill of materials of its own replaces it - see resolveBom for
-- why that is all-or-nothing rather than a merge.
CREATE TABLE IF NOT EXISTS product_bom (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- RESTRICT, like variant_bom: deleting a part that products are built
    -- from must fail loudly rather than empty a kit nobody was looking at.
    inventory_item_id uuid NOT NULL REFERENCES inventory_items (id) ON DELETE RESTRICT,
    quantity   numeric(12, 3) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_product_bom_item
    ON product_bom (product_id, inventory_item_id);

-- +goose Down
DROP TABLE IF EXISTS product_bom;
