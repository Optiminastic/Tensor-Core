-- name: ListIntegrationSettings :many
-- Every setting one provider has for one brand.
SELECT * FROM integration_settings
WHERE brand_slug = sqlc.arg('brand_slug') AND provider = sqlc.arg('provider')
ORDER BY setting_key;

-- name: UpsertIntegrationSetting :exec
-- Writes one setting. Upsert because the form saves the whole set each time
-- and a half-written provider is worse than an unwritten one.
INSERT INTO integration_settings (
    brand_slug, provider, setting_key, setting_value, is_secret, updated_by, updated_at
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('provider'), sqlc.arg('setting_key'),
    sqlc.arg('setting_value'), sqlc.arg('is_secret'), sqlc.narg('updated_by'), now()
)
ON CONFLICT (brand_slug, provider, setting_key) DO UPDATE SET
    setting_value = excluded.setting_value,
    is_secret     = excluded.is_secret,
    updated_by    = excluded.updated_by,
    updated_at    = now();

-- name: DeleteIntegrationSettings :exec
-- Disconnects a provider by forgetting everything it was given.
DELETE FROM integration_settings
WHERE brand_slug = sqlc.arg('brand_slug') AND provider = sqlc.arg('provider');

-- name: ClearIntegrationSetting :exec
-- Forgets ONE setting, so a field can be emptied from the dashboard.
--
-- A row rather than a blank value: absent and empty mean the same thing to
-- every reader, and an empty string left behind reads in the table as though
-- somebody meant to put something there.
--
-- Only ever called for a non-secret field. A secret comes back masked, so a
-- blank box cannot be taken as "clear this" without wiping a token the admin
-- was never allowed to see.
DELETE FROM integration_settings
WHERE brand_slug = sqlc.arg('brand_slug')
  AND provider = sqlc.arg('provider')
  AND setting_key = sqlc.arg('setting_key');

-- name: ListBrandsWithIntegration :many
-- Which brands have this provider configured - the scheduler's own question.
SELECT DISTINCT brand_slug FROM integration_settings
WHERE provider = sqlc.arg('provider');
