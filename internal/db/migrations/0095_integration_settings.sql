-- +goose Up
-- Credentials an admin can enter, per brand, per provider.
--
-- brand_connections holds ONE token - it was built for OAuth, where that is
-- all there is. A credential-based provider needs several values (a key, a
-- host, ids from somebody else's console), so they do not fit that shape and
-- should not be bent into it.
--
-- Key-value rather than a column per field. The alternative is a migration
-- every time a provider adds a setting, and the set of settings is exactly
-- the thing that keeps moving.
--
-- SECRETS ARE SEALED (internal/secretbox, AES-256-GCM), the same way Shopify's
-- access token is. is_secret says which, so the form knows to mask them.
CREATE TABLE IF NOT EXISTS integration_settings (
    brand_slug varchar(64)  NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    provider   varchar(32)  NOT NULL,
    setting_key varchar(64) NOT NULL,
    -- Sealed when is_secret, plain otherwise.
    setting_value text      NOT NULL,
    is_secret  boolean      NOT NULL DEFAULT false,
    updated_by varchar(64),
    updated_at timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, provider, setting_key)
);

CREATE INDEX IF NOT EXISTS ix_integration_settings_provider
    ON integration_settings (brand_slug, provider);

-- +goose Down
DROP TABLE IF EXISTS integration_settings;
