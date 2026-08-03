-- +goose Up
-- SKU catalog: make the SKU a persisted, unique identifier on a design, and give
-- a design a stable template file_asset so an order's SKU can resolve straight to
-- a printable job (STL attached). Both columns are idempotent adds.

-- The design's SKU. Nullable until assigned; globally unique when set. A partial
-- unique index allows many un-SKU'd designs (NULL) while guaranteeing no two
-- designs share an assigned SKU.
ALTER TABLE designs ADD COLUMN IF NOT EXISTS sku varchar(64);
CREATE UNIQUE INDEX IF NOT EXISTS designs_sku_key ON designs (sku) WHERE sku IS NOT NULL;

-- The file_asset that stands in for this design's model in the production queue.
-- Created lazily (reusing the design's stl_key storage object) the first time an
-- order for the design's SKU is turned into jobs, then reused for every reprint.
ALTER TABLE designs ADD COLUMN IF NOT EXISTS template_file_id uuid REFERENCES file_assets (id) ON DELETE SET NULL;

-- +goose Down
DROP INDEX IF EXISTS designs_sku_key;
ALTER TABLE designs DROP COLUMN IF EXISTS template_file_id;
ALTER TABLE designs DROP COLUMN IF EXISTS sku;
