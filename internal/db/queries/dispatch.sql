-- Dispatch orders: a shipment record per order, status pending -> dispatched.

-- name: InsertDispatch :one
INSERT INTO dispatch_orders (id, order_id, carrier, tracking_number, status)
VALUES (sqlc.arg('id'), sqlc.arg('order_id'), sqlc.narg('carrier'), sqlc.narg('tracking_number'), sqlc.arg('status'))
RETURNING *;

-- name: GetDispatchByID :one
SELECT * FROM dispatch_orders WHERE id = $1;

-- name: ListDispatch :many
SELECT * FROM dispatch_orders ORDER BY created_at DESC, id DESC;

-- name: ListDispatchPage :many
SELECT * FROM dispatch_orders
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: MarkDispatched :one
UPDATE dispatch_orders SET status = 'dispatched', dispatched_at = now(), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;
