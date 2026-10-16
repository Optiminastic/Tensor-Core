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
