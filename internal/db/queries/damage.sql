-- name: FindOrderByNumber :one
-- The Tensor order behind an order number, when there is one.
--
-- Best effort by design. The bot resolves orders live against Shopify, so a
-- damage report can name an order Tensor has never imported - a brand new one,
-- or one from a shop whose sync is behind. Finding nothing is ordinary and the
-- report is filed either way.
SELECT id, customer_name FROM orders WHERE order_number = sqlc.arg('order_number') LIMIT 1;

-- name: InsertDamageReport :one
-- Files one damage report, once.
--
-- ON CONFLICT on the closing message's wamid: a River retry after the row was
-- written - or a webhook Meta replayed - must not file the same report twice.
-- Nothing returned means it already exists, and the caller stands down.
INSERT INTO damage_reports (
    brand_slug, wa_id, shopify_order_gid, order_number, order_id,
    customer_name, photo_count, wa_message_id
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('wa_id'), sqlc.arg('shopify_order_gid'),
    sqlc.arg('order_number'), sqlc.narg('order_id'), sqlc.arg('customer_name'),
    sqlc.arg('photo_count'), sqlc.arg('wa_message_id')
)
ON CONFLICT (wa_message_id) DO NOTHING
RETURNING *;

-- name: LinkDamageReportPhotos :execrows
-- Attaches the photographs this report was built from, in arrival order.
--
-- One statement, and the brand is in the WHERE clause: the wamids come from a
-- conversation's own scratchpad, and a forged one must not be able to reach
-- another brand's message. unnest WITH ORDINALITY keeps the order the customer
-- sent them in.
INSERT INTO damage_report_photos (report_id, message_id, position)
SELECT sqlc.arg('report_id'), m.id, w.ord - 1
FROM unnest(sqlc.arg('wa_message_ids')::text[]) WITH ORDINALITY AS w(wamid, ord)
JOIN whatsapp_messages m
  ON m.wa_message_id = w.wamid AND m.brand_slug = sqlc.arg('brand_slug')
ON CONFLICT DO NOTHING;

-- name: ListDamageReports :many
-- The brand's damage reports, newest first, with how many of their
-- photographs actually arrived.
--
-- THE PHOTO TALLY IS THE POINT. "Three photographs" and "three photographs,
-- none of which we hold" are different situations and the page has to tell
-- them apart, so the counts are computed here rather than inferred from
-- photo_count alone.
SELECT r.*,
       coalesce(c.linked, 0)::int   AS photos_linked,
       coalesce(c.stored, 0)::int   AS photos_stored,
       coalesce(c.failed, 0)::int   AS photos_failed,
       coalesce(c.pending, 0)::int  AS photos_pending,
       o.order_number IS NOT NULL   AS order_imported
FROM damage_reports r
LEFT JOIN orders o ON o.id = r.order_id
LEFT JOIN LATERAL (
    SELECT count(*) AS linked,
           count(*) FILTER (WHERE m.media_status = 'stored')  AS stored,
           count(*) FILTER (WHERE m.media_status = 'failed')  AS failed,
           count(*) FILTER (WHERE m.media_status = 'pending') AS pending
    FROM damage_report_photos p
    JOIN whatsapp_messages m ON m.id = p.message_id
    WHERE p.report_id = r.id
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

-- name: ListDamageReportPhotos :many
-- The photographs, in the order the customer sent them, each with where its
-- download got to. The body is the caption they typed alongside it.
SELECT m.id AS message_id, m.kind, m.body, m.media_status, m.media_mime_type,
       m.media_error, m.created_at, p.position
FROM damage_report_photos p
JOIN whatsapp_messages m ON m.id = p.message_id
WHERE p.report_id = sqlc.arg('report_id')
ORDER BY p.position;

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
