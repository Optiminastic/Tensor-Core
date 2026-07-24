-- +goose Up
-- Brands become free-form (user-created): the `brand` enum is replaced by a slug
-- on the brands table, and every brand column becomes plain text referencing it.
-- Written idempotently so it is safe on a database already carrying the two
-- seeded brands.

-- 1. brands: add slug (seeded from the old enum key) and a logo url.
ALTER TABLE brands ADD COLUMN IF NOT EXISTS slug text;
UPDATE brands SET slug = key::text WHERE slug IS NULL;
ALTER TABLE brands ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_brands_slug ON brands (slug);
ALTER TABLE brands ADD COLUMN IF NOT EXISTS logo_url text;

-- 2. project and cost-assumption brand columns: enum -> text.
ALTER TABLE projects ALTER COLUMN brand TYPE text USING brand::text;
ALTER TABLE cost_assumption_sets ALTER COLUMN brand TYPE text USING brand::text;

-- 3. drop the old enum key column, then the now-unused enum type.
ALTER TABLE brands DROP COLUMN IF EXISTS key;
DROP TYPE IF EXISTS brand;

-- 4. a project's brand must be a real brand slug.
-- +goose StatementBegin
DO $$ BEGIN
    ALTER TABLE projects
        ADD CONSTRAINT projects_brand_fkey
        FOREIGN KEY (brand) REFERENCES brands (slug) ON DELETE RESTRICT;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- +goose StatementEnd

-- 5. per-brand connections to external ad and commerce platforms.
CREATE TABLE IF NOT EXISTS brand_connections (
    id                  uuid PRIMARY KEY,
    brand_slug          text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    provider            text NOT NULL,
    status              text NOT NULL,
    external_account_id text,
    access_token        text,
    refresh_token       text,
    expires_at          timestamptz,
    connected_by        varchar(64),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_brand_connection UNIQUE (brand_slug, provider)
);

-- +goose Down
DROP TABLE IF EXISTS brand_connections;
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_brand_fkey;
-- The brand enum is not restored; this migration is one-way in practice.
