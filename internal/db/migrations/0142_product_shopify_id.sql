-- +goose Up
-- Which Shopify product a registry product is.
--
-- Orders are matched to the registry by SKU, and many storefront products have
-- none - Couple's Date Plank carries 64 variants without one - so no order for
-- them could ever reach the registry. Every order line does carry the Shopify
-- product id, which is stable across renames, so a product imported from
-- Shopify records it and an order line with no SKU is matched by it instead
-- (see resolveLineSKU).
ALTER TABLE products ADD COLUMN IF NOT EXISTS shopify_product_id varchar(32);
CREATE INDEX IF NOT EXISTS ix_products_shopify_product_id
    ON products (shopify_product_id) WHERE shopify_product_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_products_shopify_product_id;
ALTER TABLE products DROP COLUMN IF EXISTS shopify_product_id;
