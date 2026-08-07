-- Per-user brand access: which brands a member may see and work in. Admins are
-- not listed here (they access every brand); a non-admin can only reach the brands
-- with a row here. user_id holds a Better Auth id (no FK, like user_roles).

-- +goose Up
CREATE TABLE IF NOT EXISTS user_brands (
    user_id     varchar(64) NOT NULL,
    brand_slug  text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    assigned_by varchar(64) NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, brand_slug)
);
CREATE INDEX IF NOT EXISTS ix_user_brands_brand ON user_brands (brand_slug);

-- +goose Down
DROP TABLE IF EXISTS user_brands;
