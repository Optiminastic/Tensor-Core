-- +goose Up
-- Store-level access is gone: every role reaches every brand.
--
-- 0086 added user_brand_access and user_invites.brand_slugs one commit earlier,
-- to answer "which brands may this member work in". The shop's instruction is
-- that the question has no answer worth storing - everybody works in every
-- store - so listBrands and getBrand stopped filtering, and with nothing reading
-- these they became a table and a column that only ever lied: an admin ticking
-- brands on the People page would have been saving a restriction that applied
-- nowhere.
--
-- Dropped rather than left in place because a dormant access table is worse
-- than no access table. The next person to find it has to work out whether it is
-- enforced, and the honest answer has to come from reading every brand query.
--
-- Bringing store scoping back means a new migration and, more importantly,
-- filtering BOTH listBrands and getBrand - a list that hides a brand while the
-- detail route still serves it by slug is not access control.
DROP TABLE IF EXISTS user_brand_access;

ALTER TABLE user_invites DROP COLUMN IF EXISTS brand_slugs;

-- +goose Down
-- Recreated exactly as 0086 had it, so rolling back lands on that schema.
CREATE TABLE IF NOT EXISTS user_brand_access (
    user_id     varchar(64) NOT NULL,
    brand_slug  text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    granted_by  varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, brand_slug)
);
CREATE INDEX IF NOT EXISTS ix_user_brand_access_brand ON user_brand_access (brand_slug);

ALTER TABLE user_invites
    ADD COLUMN IF NOT EXISTS brand_slugs text[] NOT NULL DEFAULT '{}';
