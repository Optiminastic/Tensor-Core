-- Per-product rows for an order (SKU, name, image), additive to orders.line_items
-- jsonb. Used by both the Shopify webhook import and dummy-order seeding.

-- name: InsertOrderLineItem :one
INSERT INTO order_line_items (
    id, order_id, shopify_order_id, shopify_customer_id, sku, product_name,
    product_image_url, quantity
) VALUES (
    sqlc.arg('id'), sqlc.arg('order_id'), sqlc.arg('shopify_order_id'),
    sqlc.narg('shopify_customer_id'), sqlc.narg('sku'), sqlc.arg('product_name'),
    sqlc.narg('product_image_url'), sqlc.arg('quantity')
)
RETURNING *;

-- name: ListLineItemsForOrder :many
SELECT id, order_id, shopify_order_id, shopify_customer_id, sku, product_name,
       product_image_url, quantity, created_at
FROM order_line_items
WHERE order_id = $1
ORDER BY created_at ASC, id ASC;

-- name: DeleteLineItemsForOrder :exec
-- Lets a webhook replay (or a re-run of seeding) rebuild an order's product rows
-- from scratch instead of accumulating duplicates.
DELETE FROM order_line_items WHERE order_id = $1;
