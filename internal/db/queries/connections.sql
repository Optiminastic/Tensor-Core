-- Per-brand connections to external ad and commerce platforms. Tokens are never
-- selected back out to callers -- only status and the external account id.

-- name: ListConnectionsForBrand :many
SELECT id, brand_slug, provider, status, external_account_id,
       expires_at, connected_by, created_at, updated_at
FROM brand_connections WHERE brand_slug = $1 ORDER BY provider;

-- name: GetConnection :one
SELECT id, brand_slug, provider, status, external_account_id,
       expires_at, connected_by, created_at, updated_at
FROM brand_connections WHERE brand_slug = $1 AND provider = $2;

-- GetConnectionWithToken is the ONLY query that reads the access token back out.
-- It exists for the server-to-server publish path (e.g. creating a Shopify draft
-- product) and must never be exposed on a caller-facing endpoint.
-- name: GetConnectionWithToken :one
SELECT status, external_account_id, access_token
FROM brand_connections WHERE brand_slug = $1 AND provider = $2;

-- name: UpsertConnection :one
INSERT INTO brand_connections (
    id, brand_slug, provider, status, external_account_id,
    access_token, refresh_token, expires_at, connected_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('provider'), sqlc.arg('status'),
    sqlc.narg('external_account_id'), sqlc.narg('access_token'), sqlc.narg('refresh_token'),
    sqlc.narg('expires_at'), sqlc.narg('connected_by')
)
ON CONFLICT (brand_slug, provider) DO UPDATE SET
    status = EXCLUDED.status,
    external_account_id = EXCLUDED.external_account_id,
    access_token = EXCLUDED.access_token,
    refresh_token = EXCLUDED.refresh_token,
    expires_at = EXCLUDED.expires_at,
    connected_by = EXCLUDED.connected_by,
    updated_at = now()
RETURNING id, brand_slug, provider, status, external_account_id,
          expires_at, connected_by, created_at, updated_at;

-- name: DeleteConnection :execrows
DELETE FROM brand_connections WHERE brand_slug = $1 AND provider = $2;

-- GetShopifyConnectionByDomain lets the inbound order webhooks (webhooks.go)
-- accept a store connected only via the brand-level Shopify OAuth flow
-- (brand_connections), not just the dedicated shopify_connections one - a
-- webhook has no brand_slug to key off, only the shop domain Shopify sends.
-- name: GetShopifyConnectionByDomain :one
SELECT id FROM brand_connections
WHERE provider = 'shopify' AND status = 'connected' AND lower(external_account_id) = lower(sqlc.arg('domain'))
LIMIT 1;
