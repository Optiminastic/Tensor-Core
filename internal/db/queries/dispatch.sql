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

-- name: ListDispatchedOrderIDs :many
-- Which of the given orders have actually shipped (not merely have a
-- dispatch_orders row created) - the pipeline_stage DISPATCHED signal.
SELECT DISTINCT order_id FROM dispatch_orders
WHERE order_id = ANY(sqlc.arg('order_ids')::uuid[]) AND status = 'dispatched';

-- name: CountDispatch :one
SELECT count(*) FROM dispatch_orders;

-- name: ListDispatchTable :many
-- One page of the Dispatch table, in ListDispatch's order, cut by LIMIT/OFFSET.
SELECT * FROM dispatch_orders
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: InsertCatchUpDispatch :execrows
-- Records that an order already shipped, for the bulk catch-up.
--
-- NO CARRIER AND NO TRACKING NUMBER, deliberately. Tensor never booked these
-- shipments - they went out before any of this was used - so it does not know
-- the waybill, and inventing one would be worse than leaving it blank. The
-- real tracking lives in Shopify's fulfilments and the Deliveries page reads
-- it live from the carrier.
--
-- WHERE NOT EXISTS rather than ON CONFLICT: order_id carries no unique index
-- (an order may legitimately ship in more than one parcel), so the guard has
-- to be explicit. Running the catch-up twice must not double-record a
-- shipment.
INSERT INTO dispatch_orders (id, order_id, status, dispatched_at)
SELECT sqlc.arg('id'), sqlc.arg('order_id'), 'dispatched', now()
WHERE NOT EXISTS (
    SELECT 1 FROM dispatch_orders d WHERE d.order_id = sqlc.arg('order_id')
);
