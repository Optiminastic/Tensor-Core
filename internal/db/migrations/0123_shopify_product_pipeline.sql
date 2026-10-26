-- +goose Up
-- Slicer settings assigned to the SHOPIFY product, which every SKU has.
--
-- 0122 hung the mapping off a registry product, which is right for the two
-- products that have been imported and useless for the twenty-six SKUs that
-- actually print. Those are not parentless - the plank's twenty variants all
-- sit under "Dual Name Plank (all options)" in Shopify - they simply have no
-- row in a registry that is behind the floor.
--
-- The Shopify product id is the parent every printing SKU already has, and an
-- order line carries it, so a job can be traced back to it without asking
-- Shopify anything.
ALTER TABLE sku_slicer_pipelines
    ADD COLUMN IF NOT EXISTS shopify_product_id varchar(64);

ALTER TABLE sku_slicer_pipelines
    DROP CONSTRAINT IF EXISTS ck_sku_pipeline_target;

-- Exactly one target: a SKU, a registry product, or a Shopify product.
ALTER TABLE sku_slicer_pipelines
    ADD CONSTRAINT ck_sku_pipeline_target
        CHECK (num_nonnulls(sku, product_id, shopify_product_id) = 1);

CREATE UNIQUE INDEX IF NOT EXISTS uq_shopify_product_pipeline_family
    ON sku_slicer_pipelines (shopify_product_id, upper(machine_family))
    WHERE shopify_product_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_shopify_product_pipeline_family;
DELETE FROM sku_slicer_pipelines WHERE shopify_product_id IS NOT NULL;
ALTER TABLE sku_slicer_pipelines DROP CONSTRAINT IF EXISTS ck_sku_pipeline_target;
ALTER TABLE sku_slicer_pipelines DROP COLUMN IF EXISTS shopify_product_id;
ALTER TABLE sku_slicer_pipelines
    ADD CONSTRAINT ck_sku_pipeline_target CHECK (num_nonnulls(sku, product_id) = 1);
