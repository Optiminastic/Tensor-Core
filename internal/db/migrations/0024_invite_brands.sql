-- An invite carries the brands the admin assigned, so that on acceptance the new
-- member is granted access to exactly those brands (user_brands rows). Stored as a
-- jsonb array of brand slugs; empty array means no brand access yet.

-- +goose Up
ALTER TABLE user_invites ADD COLUMN IF NOT EXISTS brand_slugs jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE user_invites DROP COLUMN IF EXISTS brand_slugs;
