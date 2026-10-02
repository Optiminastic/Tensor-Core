-- Bulk orders and their lines.

-- name: InsertBulkOrder :one
INSERT INTO bulk_orders (
    id, quotation_number, brand_slug, customer_name, customer_email, customer_phone,
    notes, order_date, valid_until, status, discount_percent,
    subtotal, discount_amount, total, created_by
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
RETURNING *;

-- name: UpdateBulkOrder :one
-- The header, re-saved. The lines are replaced separately (delete then insert),
-- because an edit can add, remove and reorder them at once and a diff would be
-- more code than a rewrite for a list this short.
UPDATE bulk_orders SET
    customer_name = $2, customer_email = $3, customer_phone = $4,
    notes = $5, order_date = $6, valid_until = $7, status = $8,
    discount_percent = $9, subtotal = $10, discount_amount = $11, total = $12,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetBulkOrder :one
SELECT * FROM bulk_orders WHERE id = $1;

-- name: ListBulkOrders :many
-- Newest first: this feeds a screen, and the thing somebody just created is the
-- one they are looking for.
SELECT * FROM bulk_orders
WHERE brand_slug = $1
ORDER BY created_at DESC, id DESC;

-- name: DeleteBulkOrder :exec
DELETE FROM bulk_orders WHERE id = $1;

-- name: InsertBulkOrderLine :exec
INSERT INTO bulk_order_lines (
    id, bulk_order_id, variant_id, sku, product_name, quantity, unit_price, line_total, position
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: DeleteBulkOrderLines :exec
DELETE FROM bulk_order_lines WHERE bulk_order_id = $1;

-- name: ListBulkOrderLines :many
SELECT * FROM bulk_order_lines WHERE bulk_order_id = $1 ORDER BY position, id;

-- name: ListBulkOrderLineCounts :many
-- How many lines each order holds, for the list screen - one query rather than
-- one per row.
SELECT bulk_order_id, count(*)::int AS lines, coalesce(sum(quantity), 0)::int AS units
FROM bulk_order_lines
GROUP BY bulk_order_id;

-- name: QuotationNumberExists :one
SELECT EXISTS (SELECT 1 FROM bulk_orders WHERE quotation_number = $1) AS taken;
