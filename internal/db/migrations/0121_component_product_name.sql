-- +goose Up
-- The name of the product a component belongs to, beside its gid.
--
-- A SNAPSHOT, not a join, for the same reason sku_slicer_pipelines stores
-- pipeline_name (migration 0085): the gid alone cannot be read by a person,
-- and resolving it would mean a live Shopify call on every registry page load
-- to render one line of text.
--
-- It goes stale if the product is renamed in Shopify, which is the trade. The
-- alternative - a picker that lists every component on the shelf with no sign
-- of what each belongs to - is how a diya cup gets linked to the wrong holder.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS shopify_product_title varchar(255);

-- +goose Down
ALTER TABLE inventory_items DROP COLUMN IF EXISTS shopify_product_title;
