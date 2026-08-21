-- name: InsertShopifyApiCall :exec
-- Records one physical Shopify Admin API request. Best-effort audit log; the
-- caller ignores the error so logging never fails a publish.
INSERT INTO shopify_api_calls (
    id, shop_domain, brand_slug, method, operation, scope,
    status_code, ok, error_message, latency_ms
) VALUES (
    sqlc.arg('id'), sqlc.arg('shop_domain'), sqlc.narg('brand_slug'), sqlc.arg('method'),
    sqlc.arg('operation'), sqlc.arg('scope'), sqlc.arg('status_code'), sqlc.arg('ok'),
    sqlc.narg('error_message'), sqlc.arg('latency_ms')
);

-- name: ListShopifyApiCallsPage :many
-- Keyset page: rows strictly before the (created_at, id) cursor, newest first. A
-- null cursor returns the first page. Optional filters: shop_domain (empty = all)
-- and only_errors (true = failed calls only).
SELECT id, shop_domain, brand_slug, method, operation, scope,
       status_code, ok, error_message, latency_ms, created_at
FROM shopify_api_calls
WHERE (sqlc.arg('shop_domain')::text = '' OR shop_domain = sqlc.arg('shop_domain')::text)
  AND (NOT sqlc.arg('only_errors')::bool OR ok = false)
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');
