-- +goose Up
-- Backs the global "all brands" designs view: ListDesignsForBrands(Page) filter
-- by `brand_slug = ANY(...)` and order by created_at DESC, id DESC across every
-- brand. The existing ix_designs_brand_created leads with brand_slug, so it does
-- not serve a cross-brand ordering; this brand-agnostic composite does. Idempotent.
CREATE INDEX IF NOT EXISTS ix_designs_created ON designs (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS ix_designs_created;
