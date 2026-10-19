-- name: FindOrderByNumber :one
-- The Tensor order behind whatever the customer typed, when there is one.
--
-- TWO WAYS OF SAYING THE SAME ORDER. They are asked for "your order ID" and
-- the confirmation email shows T3DPS-115766, so some type that and some type
-- 115766 - and orders.order_number holds the full prefixed form. Matching on
-- the exact text alone finds the first group and not the second.
--
-- The digit comparison is a scan, and that is affordable here: it runs once
-- per damage report, of which there are tens a month, and only when the
-- exact match has already missed.
--
-- Best effort throughout. A customer can name an order Tensor has never
-- imported - a brand new one, one from a shop whose sync is behind, or a
-- number they mistyped - and the report is filed either way.
SELECT id, customer_name FROM orders
WHERE order_number = sqlc.arg('typed')
   OR (sqlc.arg('digits')::text <> ''
       AND regexp_replace(order_number, '\D', '', 'g') = sqlc.arg('digits')::text)
ORDER BY (order_number = sqlc.arg('typed')) DESC, created_at DESC
LIMIT 1;

-- name: UpsertDamageReportForLink :one
-- Files the report behind one link, or updates the one already there.
--
-- ONE REPORT PER LINK, which is the whole deduplication story now that the
-- old wamid key is gone. A customer who reopens their link inside its 24
-- hours and sends another photograph is correcting the report they already
-- filed, not making a second one - so a resubmission updates the order
-- number they typed and the tally, and leaves the status and the resolution
-- note alone. Somebody in the shop may already have moved it to in_review.
INSERT INTO damage_reports (
    brand_slug, wa_id, order_number, order_id, customer_name,
    photo_count, link_id
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('wa_id'), sqlc.arg('order_number'),
    sqlc.narg('order_id'), sqlc.arg('customer_name'),
    sqlc.arg('photo_count'), sqlc.arg('link_id')
)
ON CONFLICT (link_id) WHERE link_id IS NOT NULL DO UPDATE
SET order_number  = excluded.order_number,
    order_id      = coalesce(excluded.order_id, damage_reports.order_id),
    customer_name = CASE WHEN excluded.customer_name <> ''
                         THEN excluded.customer_name ELSE damage_reports.customer_name END,
    photo_count   = excluded.photo_count,
    updated_at    = now()
RETURNING *;

-- name: ListDamageReports :many
-- The brand's damage reports, newest first, with how many files each holds.
--
-- ONE TALLY, not four. The old list carried stored/pending/failed because a
-- WhatsApp attachment had to be fetched out of Meta and could still be in
-- flight or have failed. A file the customer uploaded to the form is simply
-- there.
SELECT r.*,
       coalesce(c.files, 0)::int AS file_count,
       o.order_number IS NOT NULL AS order_imported
FROM damage_reports r
LEFT JOIN orders o ON o.id = r.order_id
LEFT JOIN LATERAL (
    SELECT count(*) AS files FROM damage_files d WHERE d.link_id = r.link_id
) c ON true
WHERE r.brand_slug = sqlc.arg('brand_slug')
ORDER BY r.reported_at DESC
LIMIT sqlc.arg('row_limit');

-- name: CountDamageReportsByStatus :many
-- The tab counts, over the whole brand rather than the page.
SELECT status, count(*)::int AS total
FROM damage_reports WHERE brand_slug = sqlc.arg('brand_slug')
GROUP BY status;

-- name: GetDamageReport :one
SELECT * FROM damage_reports
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug');

-- name: ListDamageReportFiles :many
-- The photographs and videos a report holds, in the order they were uploaded.
--
-- Reached through the report's own link, which is also what scopes them: the
-- files belong to the link, and the link belongs to the brand.
SELECT f.id, f.filename, f.content_type, f.size_bytes, d.position, d.created_at
FROM damage_reports r
JOIN damage_files d ON d.link_id = r.link_id
JOIN file_assets f ON f.id = d.file_id
WHERE r.id = sqlc.arg('report_id')
ORDER BY d.position;

-- name: UpdateDamageReportStatus :one
-- Moves a report along, records a resolution, or both.
--
-- Each field is optional and coalesced, so sending only a note does not reset
-- the status and sending only a status does not wipe the note.
UPDATE damage_reports
SET status            = coalesce(sqlc.narg('status'), status),
    resolution_note   = coalesce(sqlc.narg('resolution_note'), resolution_note),
    status_changed_at = now(),
    status_changed_by = sqlc.arg('status_changed_by'),
    updated_at        = now()
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug')
RETURNING *;

-- name: GetDamageReportByLink :one
-- The report already filed against this link, if any.
--
-- Read by the public form so that reopening the link shows what the customer
-- already sent, rather than an empty box asking for the order number again.
SELECT * FROM damage_reports WHERE link_id = sqlc.arg('link_id');
