-- +goose Up
-- Approve a priced design and push it to Shopify as a draft product.
--
-- 1. design_pricing records who approved it, when, and at what selling price.
-- 2. shopify_products holds the reference to the created draft product (one row
--    per design), so the UI can link straight to the store to finish it.
-- Idempotent.

ALTER TABLE design_pricing
    ADD COLUMN IF NOT EXISTS approved_sp integer,
    ADD COLUMN IF NOT EXISTS approved_by varchar(64),
    ADD COLUMN IF NOT EXISTS approved_at timestamptz;

CREATE TABLE IF NOT EXISTS shopify_products (
    design_id    uuid PRIMARY KEY REFERENCES designs (id) ON DELETE CASCADE,
    brand_slug   text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    product_gid  text NOT NULL,
    handle       text NOT NULL DEFAULT '',
    admin_url    text NOT NULL DEFAULT '',
    status       text NOT NULL,
    published_by varchar(64),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS shopify_products;
ALTER TABLE design_pricing
    DROP COLUMN IF EXISTS approved_sp,
    DROP COLUMN IF EXISTS approved_by,
    DROP COLUMN IF EXISTS approved_at;
