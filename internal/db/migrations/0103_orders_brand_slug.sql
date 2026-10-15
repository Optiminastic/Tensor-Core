-- +goose Up
-- Which brand an order belongs to, on the order itself.
--
-- Orders had no usable link to a brand, and the one that looked like a link
-- was broken in two ways at once:
--
--   * orders.shop_connection_id REFERENCES shopify_connections(id), while the
--     query that reads it joins brand_connections ON c.id = shop_connection_id.
--     Two different tables, so the join could only ever match by coincidence.
--   * The routine order sync passes no connection id at all - it runs from a
--     brand_connections row and has no shopify_connections row to reference -
--     so the column is NULL on every order it imports anyway.
--
-- With one brand connected the fallback hid all of this: "there is only one
-- shop, so it must be that one". The day a second brand was connected the
-- fallback stopped being able to guess, and every new order became
-- unattributable - which took the personalisation check down with it, because
-- a brand is how it finds the WhatsApp credentials and the watermark. The log
-- said so plainly on every order: "this order's connection is gone and 2
-- brands are connected, so it cannot be attributed".
--
-- No foreign key to brands(slug). The import must not fail because a brand row
-- is missing or renamed - an order that arrived is a fact, and losing it to
-- keep a reference tidy is the wrong trade. The column is advisory and the
-- readers treat an unknown slug as "not attributable", which is what they did
-- before.
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS brand_slug text;

CREATE INDEX IF NOT EXISTS ix_orders_brand_slug
    ON orders (brand_slug) WHERE brand_slug IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_orders_brand_slug;
ALTER TABLE orders DROP COLUMN IF EXISTS brand_slug;
