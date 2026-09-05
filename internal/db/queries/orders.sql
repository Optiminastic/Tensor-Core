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
RETURNING *;

-- name: UpsertPaidOrder :one
-- Idempotent import from the orders/paid webhook: keyed on shopify_order_id, a
-- replay only refreshes the mutable financial fields. source defaults to
-- 'shopify_webhook', so a webhook-imported order is never mistaken for seed data.
INSERT INTO orders (
    id, shop_connection_id, shopify_order_id, order_number, customer_name,
    shopify_customer_id, customer_email, customer_phone,
    financial_status, total_price, currency, line_items, status,
    placed_at, note, attributes, tags, fulfillment_status, source_name,
    delivery_status, return_status,
    subtotal_price, total_discounts, total_shipping, total_received,
    discount_title, shipping_title, shipping_address, billing_address
) VALUES (
    sqlc.arg('id'), sqlc.narg('shop_connection_id'), sqlc.arg('shopify_order_id'),
    sqlc.arg('order_number'), sqlc.narg('customer_name'),
    sqlc.narg('shopify_customer_id'), sqlc.narg('customer_email'), sqlc.narg('customer_phone'),
    sqlc.arg('financial_status'), sqlc.arg('total_price')::float8, sqlc.arg('currency'),
    sqlc.arg('line_items'), sqlc.arg('status'),
    sqlc.narg('placed_at'), sqlc.narg('note'), sqlc.arg('attributes'), sqlc.arg('tags'),
    sqlc.narg('fulfillment_status'), sqlc.narg('source_name'),
    sqlc.narg('delivery_status'), sqlc.narg('return_status'),
    sqlc.narg('subtotal_price')::numeric, sqlc.narg('total_discounts')::numeric,
    sqlc.narg('total_shipping')::numeric, sqlc.narg('total_received')::numeric,
    sqlc.narg('discount_title'), sqlc.narg('shipping_title'),
    sqlc.narg('shipping_address'), sqlc.narg('billing_address')
)
ON CONFLICT (shopify_order_id) DO UPDATE SET
    financial_status = EXCLUDED.financial_status,
    total_price      = EXCLUDED.total_price,
    -- Refreshed too, so a re-sync repairs an order rather than only touching
    -- its payment state. Shopify is the source of truth for what the customer
    -- ordered, and Tensor never edits line_items after import - production
    -- jobs are separate rows derived from it.
    --
    -- Without this, improving the import only ever helped orders that had not
    -- arrived yet: every order already in the table kept whatever the mapper
    -- of the day happened to understand, with no way to backfill short of
    -- deleting and re-importing.
    line_items       = EXCLUDED.line_items,
    -- The whole Shopify picture is refreshed for the same reason line_items is:
    -- Shopify owns these facts, Tensor never edits them, and an order already
    -- in the table must be able to gain the detail a later import learned to
    -- read. Pressing Sync is how an operator repairs an order.
    placed_at          = EXCLUDED.placed_at,
    note               = EXCLUDED.note,
    attributes         = EXCLUDED.attributes,
    tags               = EXCLUDED.tags,
    fulfillment_status = EXCLUDED.fulfillment_status,
    delivery_status    = EXCLUDED.delivery_status,
    return_status      = EXCLUDED.return_status,
    source_name        = EXCLUDED.source_name,
    subtotal_price     = EXCLUDED.subtotal_price,
    total_discounts    = EXCLUDED.total_discounts,
    total_shipping     = EXCLUDED.total_shipping,
    total_received     = EXCLUDED.total_received,
    discount_title     = EXCLUDED.discount_title,
    shipping_title     = EXCLUDED.shipping_title,
    -- Address columns are only ever overwritten with something. Protected
    -- customer data arrives null until Shopify grants access, and letting a
    -- null flatten an address a previous sync captured would lose it.
    shipping_address   = COALESCE(EXCLUDED.shipping_address, orders.shipping_address),
    billing_address    = COALESCE(EXCLUDED.billing_address, orders.billing_address),
    updated_at       = now()
RETURNING *;

-- name: GetOrderByID :one
SELECT * FROM orders WHERE id = $1;

-- name: ListOrders :many
-- A null source returns every order regardless of origin.
SELECT * FROM orders
WHERE sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source')
-- Newest ORDER first, not newest import.
--
-- imported_at is when Tensor happened to fetch a row, which on a backfill is
-- the same second for hundreds of them - so it sorted by nothing at all and
-- the list read as shuffled: 114762, 114763, 114743, 114603. placed_at is the
-- customer's own date and is what "latest first" means to anyone reading it.
-- COALESCE keeps orders imported before placed_at existed in a sensible place
-- rather than dropping them to the bottom.
ORDER BY COALESCE(placed_at, imported_at) DESC, id DESC;

-- name: ListOrdersPage :many
-- Keyset page over (imported_at, id), newest first. A null cursor returns the
-- first page. A null source returns every order regardless of origin.
SELECT * FROM orders
WHERE (
    sqlc.narg('cursor_imported_at')::timestamptz IS NULL
    OR (imported_at, id) < (sqlc.narg('cursor_imported_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
AND (sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source'))
ORDER BY imported_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: MarkOrderJobCreationFailed :exec
-- Records why job creation gave up on this order. Written only after River's
-- final attempt - before that the job is still going to be retried and an error
-- would be noise.
UPDATE orders
SET job_creation_error = sqlc.arg('reason'), job_creation_failed_at = now(), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ClearOrderJobCreationFailure :exec
-- Clears the marker once jobs are successfully created, which makes the
-- existing POST /production-jobs/from-order/:order_id the retry mechanism - no
-- separate retry endpoint needed. Guarded so a healthy order is never touched.
UPDATE orders
SET job_creation_error = NULL, job_creation_failed_at = NULL, updated_at = now()
WHERE id = sqlc.arg('id') AND job_creation_error IS NOT NULL;

-- name: ListOrdersWithoutJobs :many
-- The operator backstop, covering what the marker column cannot: an order whose
-- River job was never enqueued at all (jobEnqueuer nil), and one whose
-- line_items were empty so the worker "succeeded" with zero jobs. The column
-- records why the worker gave up; this finds orders it never reached. Both are
-- needed - they cover disjoint failure modes.
SELECT * FROM orders o
WHERE NOT EXISTS (SELECT 1 FROM production_jobs j WHERE j.order_id = o.id)
  AND (sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source'))
ORDER BY imported_at DESC, id DESC;

