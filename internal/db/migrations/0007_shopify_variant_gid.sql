-- +goose Up
-- Persist the default variant's GID alongside the created product so a publish
-- retry can reuse the already-created draft (and re-set its price) instead of
-- creating a duplicate product. Idempotent.
ALTER TABLE shopify_products ADD COLUMN IF NOT EXISTS variant_gid text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE shopify_products DROP COLUMN IF EXISTS variant_gid;
