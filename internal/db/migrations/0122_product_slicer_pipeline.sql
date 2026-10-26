-- +goose Up
-- A slicer pipeline may be assigned to a PRODUCT, and every SKU inherits it.
--
-- 0085 keyed the mapping on the SKU string, which is right for the floor - a
-- job carries a SKU and nothing else - and wrong for the person setting it up.
-- The Lotus sells in three colourways and the Royal in three; the plank will
-- have forty-four. Every one of them prints with the same settings, so a
-- mapping per SKU is the same answer typed out as many times as the shop has
-- colours, and as many chances for one of them to drift.
--
-- Exactly one of sku and product_id, like ck_variant_design_target: a row is a
-- SKU rule or a product default, never both and never neither.
--
-- Nothing is migrated because nothing is mapped: this table is empty on this
-- database, and the floor runs on each machine class's default.
ALTER TABLE sku_slicer_pipelines ALTER COLUMN sku DROP NOT NULL;

ALTER TABLE sku_slicer_pipelines
    ADD COLUMN IF NOT EXISTS product_id uuid REFERENCES products (id) ON DELETE CASCADE;

ALTER TABLE sku_slicer_pipelines
    DROP CONSTRAINT IF EXISTS ck_sku_pipeline_target;

ALTER TABLE sku_slicer_pipelines
    ADD CONSTRAINT ck_sku_pipeline_target CHECK (num_nonnulls(sku, product_id) = 1);

-- One unique index becomes two: a NULL sku does not collide in a plain unique
-- index, so the product-level row would stop being unique the moment a SKU
-- declared the same machine class.
DROP INDEX IF EXISTS uq_sku_pipeline_family;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sku_pipeline_family
    ON sku_slicer_pipelines (lower(sku), upper(machine_family))
    WHERE sku IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_product_pipeline_family
    ON sku_slicer_pipelines (product_id, upper(machine_family))
    WHERE product_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_product_pipeline_family;
DROP INDEX IF EXISTS uq_sku_pipeline_family;
DELETE FROM sku_slicer_pipelines WHERE sku IS NULL;
ALTER TABLE sku_slicer_pipelines DROP CONSTRAINT IF EXISTS ck_sku_pipeline_target;
ALTER TABLE sku_slicer_pipelines DROP COLUMN IF EXISTS product_id;
ALTER TABLE sku_slicer_pipelines ALTER COLUMN sku SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sku_pipeline_family
    ON sku_slicer_pipelines (lower(sku), upper(machine_family));
