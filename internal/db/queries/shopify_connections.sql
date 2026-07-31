-- Shopify store connections (inbound order import). The access token is stored
-- encrypted (secretbox); these queries move the ciphertext, never the plaintext.

-- name: InsertShopifyConnection :one
INSERT INTO shopify_connections (
    id, shop_domain, encrypted_access_token, scopes, webhook_subscription_id, is_active
) VALUES (
    sqlc.arg('id'), sqlc.arg('shop_domain'), sqlc.arg('encrypted_access_token'),
    sqlc.narg('scopes'), sqlc.narg('webhook_subscription_id'), true
)
RETURNING *;

-- name: GetActiveConnectionByDomain :one
SELECT * FROM shopify_connections WHERE shop_domain = $1 AND is_active;

-- name: GetConnectionByID :one
SELECT * FROM shopify_connections WHERE id = $1;

-- name: ListActiveConnections :many
SELECT * FROM shopify_connections WHERE is_active ORDER BY connected_at DESC, id DESC;

-- name: DeactivateConnection :one
UPDATE shopify_connections SET is_active = false, disconnected_at = now(), updated_at = now()
WHERE id = sqlc.arg('id') AND is_active
RETURNING *;
