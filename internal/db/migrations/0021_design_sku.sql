-- +goose Up
-- The catalog SKU a design is sold under. Nullable (not every design is
-- assigned one yet) and unique when set - the partial index lets any number
-- of not-yet-assigned designs coexist with NULL, matching the "empty string
-- clears it" contract the frontend's SKU dialog already implements.
ALTER TABLE designs ADD COLUMN IF NOT EXISTS sku varchar(64);
CREATE UNIQUE INDEX IF NOT EXISTS uq_designs_sku ON designs (sku) WHERE sku IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_designs_sku;
ALTER TABLE designs DROP COLUMN IF EXISTS sku;
