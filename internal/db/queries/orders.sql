-- Orders imported from Shopify. total_price cast to float8; line_items is jsonb
-- ([]byte in Go). InsertOrder is used for seeding/testing in Phase 1; the Shopify
-- webhook upsert arrives with the Shopify phase. source distinguishes a real
-- webhook-imported order from a seeded dummy one; both share this table so the
-- frontend can toggle between them via ListOrders/ListOrdersPage's source filter.

-- name: InsertOrder :one
INSERT INTO orders (
    id, shop_connection_id, shopify_order_id, order_number, customer_name,
    shopify_customer_id, customer_email, customer_phone,
    financial_status, total_price, currency, line_items, status, source
) VALUES (
    sqlc.arg('id'), sqlc.narg('shop_connection_id'), sqlc.arg('shopify_order_id'),
    sqlc.arg('order_number'), sqlc.narg('customer_name'),
    sqlc.narg('shopify_customer_id'), sqlc.narg('customer_email'), sqlc.narg('customer_phone'),
    sqlc.arg('financial_status'), sqlc.arg('total_price')::float8, sqlc.arg('currency'),
    sqlc.arg('line_items'), sqlc.arg('status'), sqlc.arg('source')
)
RETURNING id, shop_connection_id, shopify_order_id, order_number, customer_name,
          shopify_customer_id, customer_email, customer_phone,
          financial_status, total_price, currency, line_items,
          status, source, imported_at, created_at, updated_at;

-- name: UpsertPaidOrder :one
-- Idempotent import from the orders/paid webhook: keyed on shopify_order_id, a
-- replay only refreshes the mutable financial fields. source defaults to
-- 'shopify_webhook', so a webhook-imported order is never mistaken for seed data.
INSERT INTO orders (
    id, shop_connection_id, shopify_order_id, order_number, customer_name,
    shopify_customer_id, customer_email, customer_phone,
    financial_status, total_price, currency, line_items, status
) VALUES (
    sqlc.arg('id'), sqlc.narg('shop_connection_id'), sqlc.arg('shopify_order_id'),
    sqlc.arg('order_number'), sqlc.narg('customer_name'),
    sqlc.narg('shopify_customer_id'), sqlc.narg('customer_email'), sqlc.narg('customer_phone'),
    sqlc.arg('financial_status'), sqlc.arg('total_price')::float8, sqlc.arg('currency'),
    sqlc.arg('line_items'), sqlc.arg('status')
)
ON CONFLICT (shopify_order_id) DO UPDATE SET
    financial_status = EXCLUDED.financial_status,
    total_price      = EXCLUDED.total_price,
    updated_at       = now()
RETURNING *;

-- name: GetOrderByID :one
SELECT id, shop_connection_id, shopify_order_id, order_number, customer_name,
       shopify_customer_id, customer_email, customer_phone,
       financial_status, total_price, currency, line_items,
       status, source, imported_at, created_at, updated_at
FROM orders WHERE id = $1;

-- name: ListOrders :many
-- A null source returns every order regardless of origin.
SELECT id, shop_connection_id, shopify_order_id, order_number, customer_name,
       shopify_customer_id, customer_email, customer_phone,
       financial_status, total_price, currency, line_items,
       status, source, imported_at, created_at, updated_at
FROM orders
WHERE sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source')
ORDER BY imported_at DESC, id DESC;

-- name: ListOrdersPage :many
-- Keyset page over (imported_at, id), newest first. A null cursor returns the
-- first page. A null source returns every order regardless of origin.
SELECT id, shop_connection_id, shopify_order_id, order_number, customer_name,
       shopify_customer_id, customer_email, customer_phone,
       financial_status, total_price, currency, line_items,
       status, source, imported_at, created_at, updated_at
FROM orders
WHERE (
    sqlc.narg('cursor_imported_at')::timestamptz IS NULL
    OR (imported_at, id) < (sqlc.narg('cursor_imported_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
AND (sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source'))
ORDER BY imported_at DESC, id DESC
LIMIT sqlc.arg('page_limit');
