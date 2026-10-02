-- +goose Up
-- Which Shopify product a quotation line belongs to.
--
-- The personalisation workbook has one sheet per PRODUCT, and a product is not
-- a SKU: an order for DNP in three colours is three lines and one sheet. For a
-- line that matches a registry variant the product is known from the variant;
-- for the many that do not - the registry holds 15 SKUs where the store holds
-- 507 - there was nothing at all to group by, so every such line was skipped
-- and a real order produced an empty workbook.
--
-- Snapshotted at save time rather than re-read from Shopify when the template
-- is built, for the same reason the price is: the sheet layout must not change
-- under a quotation because somebody renamed a product in the storefront.
ALTER TABLE bulk_order_lines
    ADD COLUMN IF NOT EXISTS product_group varchar(300) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE bulk_order_lines DROP COLUMN IF EXISTS product_group;
