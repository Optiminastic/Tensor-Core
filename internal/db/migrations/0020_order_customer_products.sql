-- +goose Up
-- Customer contact details beyond the existing customer_name, plus a way to
-- tell a real Shopify-imported order apart from a seeded dummy one (both live
-- in the same table so the frontend can toggle between them).
ALTER TABLE orders
    ADD COLUMN shopify_customer_id bigint,
    ADD COLUMN customer_email varchar(255),
    ADD COLUMN customer_phone varchar(64),
    ADD COLUMN source varchar(20) NOT NULL DEFAULT 'shopify_webhook'
        CHECK (source IN ('shopify_webhook', 'seed'));
CREATE INDEX IF NOT EXISTS ix_orders_source ON orders (source);

-- Per-product rows for an order, additive to the existing orders.line_items
-- jsonb (which the production pipeline reads for print/personalisation facts
-- and is left untouched). This is the commerce-facing shape: SKU, name, image.
CREATE TABLE IF NOT EXISTS order_line_items (
    id                  uuid PRIMARY KEY,
    order_id            uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    shopify_order_id    bigint NOT NULL,
    shopify_customer_id bigint,
    sku                 varchar(120),
    product_name        varchar(255) NOT NULL,
    product_image_url   text,
    quantity            integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_order_line_items_order ON order_line_items (order_id);
CREATE INDEX IF NOT EXISTS ix_order_line_items_shopify_order ON order_line_items (shopify_order_id);

-- +goose Down
DROP TABLE IF EXISTS order_line_items;
ALTER TABLE orders
    DROP COLUMN IF EXISTS source,
    DROP COLUMN IF EXISTS customer_phone,
    DROP COLUMN IF EXISTS customer_email,
    DROP COLUMN IF EXISTS shopify_customer_id;
