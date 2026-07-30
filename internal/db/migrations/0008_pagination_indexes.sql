-- +goose Up
-- Composite indexes backing keyset pagination: the list endpoints order by
-- created_at DESC with id as the tiebreaker, filtered (designs) by brand. These
-- let a page seek directly to the cursor instead of scanning. Idempotent.
CREATE INDEX IF NOT EXISTS ix_designs_brand_created ON designs (brand_slug, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS ix_user_invites_created ON user_invites (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS ix_designs_brand_created;
DROP INDEX IF EXISTS ix_user_invites_created;
