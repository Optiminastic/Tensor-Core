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
    id, bulk_order_id, variant_id, sku, product_name, quantity, unit_price, line_total,
    position, product_group
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);

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

-- name: InsertBulkProductionJob :one
-- One production job from one spreadsheet row.
--
-- Deliberately its own insert rather than a widened InsertProductionJob: a bulk
-- job has no Shopify order, no line item and no proof to confirm, so nearly
-- half of that statement's fifty-eight arguments would be nulls threaded
-- through the storefront path for a case it does not serve. The columns here
-- are the ones a bulk job actually has.
--
-- personalisation_status is 'validated': the names came from a spreadsheet the
-- customer supplied and Tensor has just checked, so there is no proof to send
-- and nothing for an operator to confirm. Leaving it pending would park every
-- bulk job in a queue waiting for an approval that is never coming.
INSERT INTO production_jobs (
    id, job_number, bulk_order_id, description, quantity, status,
    assembly_status, qc_status, packaging_status,
    sku, product_name, colour, customer_name,
    personalisation_status, personalisation_properties,
    name_confirmed, photo_confirmed, font_confirmed, colour_confirmed,
    variant_confirmed, customer_approval_received, held, priority, colours,
    issue_reason
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_number'), sqlc.arg('bulk_order_id'),
    sqlc.arg('description'), sqlc.arg('quantity'), 'queued',
    'pending', 'pending', 'pending',
    sqlc.narg('sku'), sqlc.narg('product_name'), sqlc.narg('colour'), sqlc.narg('customer_name'),
    -- not_required, matching a generated Shopify job: the names are the INPUTS
    -- OpenSCAD builds the model from, not preferences somebody checks against a
    -- proof. Left pending, every bulk job would sit behind a confirmation with
    -- nothing to confirm.
    'not_required', sqlc.arg('personalisation_properties'),
    true, true, true, true, true, true, false, 0, sqlc.arg('colours'),
    -- stl_missing, exactly as a generated storefront job starts: it is what
    -- keeps the job out of batching until its model exists, and what the model
    -- generator looks for. Cleared by the render, after which the job batches
    -- like any other.
    sqlc.narg('issue_reason')
)
-- Returned so the caller can schedule the model render, which has to happen
-- after the transaction commits.
RETURNING *;

-- name: UpdateBulkOrderStatus :one
UPDATE bulk_orders SET status = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ApproveBulkOrder :one
-- Stamp the job code and mark the order accepted, together.
UPDATE bulk_orders SET status = 'accepted', job_code = $2, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: JobCodeTaken :one
-- Whether another order already uses this code. Checked before approving so a
-- clash is reported against the input rather than discovered when the unique
-- index rejects the hundredth job.
SELECT EXISTS (
    SELECT 1 FROM bulk_orders
    WHERE upper(job_code) = upper($1) AND id <> $2
) AS taken;

-- name: CountJobsForBulkOrder :one
SELECT count(*)::int AS jobs FROM production_jobs WHERE bulk_order_id = $1;
