-- name: ClaimPersonalisationLink :one
-- Takes the right to message this ORDER, or reports that it is already taken.
--
-- Inserted BEFORE the message is sent, like the win-back claim: a row written
-- afterwards leaves a window in which the process dies with the customer's
-- phone buzzing and nothing recorded, and the next pass messages them again.
-- Claiming first means a crash costs a missed link, which is recoverable,
-- rather than a duplicate, which is not.
--
-- DO NOTHING on conflict, so a second worker - or the next re-import of the
-- same order - gets no row back and stands down.
INSERT INTO personalisation_links (
    id, brand_slug, order_id, token_hash, phone, status, expires_at
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('order_id'),
    sqlc.arg('token_hash'), sqlc.arg('phone'), 'pending', sqlc.arg('expires_at')
)
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- name: SettlePersonalisationLink :one
-- Records how the send went.
UPDATE personalisation_links
SET status     = sqlc.arg('status'),
    sent_at    = CASE WHEN sqlc.arg('status') = 'sent' THEN now() ELSE sent_at END,
    message_id = coalesce(sqlc.narg('message_id'), message_id),
    detail     = sqlc.narg('detail')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ReleasePersonalisationLink :exec
-- Gives the order back, for a link whose message never left.
--
-- ONLY FOR A TRANSIENT REFUSAL - rate limiting, Meta down. A permanent one is
-- settled as 'failed' and KEPT, because the row is the only thing that can
-- tell anybody why a customer was never asked. Guarded on a null message_id,
-- which is the proof that nothing was sent.
DELETE FROM personalisation_links
WHERE id = sqlc.arg('id') AND message_id IS NULL;

-- name: GetPersonalisationLinkByTokenHash :one
-- The public page's own read. Expiry is judged in Go, not here, so that an
-- expired link and an unknown one can be refused with the same sentence.
SELECT * FROM personalisation_links WHERE token_hash = sqlc.arg('token_hash');

-- name: GetPersonalisationLinkByOrder :one
SELECT * FROM personalisation_links WHERE order_id = sqlc.arg('order_id');

-- name: MarkPersonalisationLinkOpened :exec
-- First open only. coalesce keeps the first timestamp, because "when did they
-- look" is answered by the first time they did; a refresh is not a new visit.
UPDATE personalisation_links
SET opened_at = coalesce(opened_at, now()),
    status    = CASE WHEN status = 'sent' THEN 'opened' ELSE status END
WHERE id = sqlc.arg('id');

-- name: ConfirmPersonalisationLink :exec
UPDATE personalisation_links
SET status = 'confirmed', confirmed_at = now()
WHERE id = sqlc.arg('id');

-- name: ListExpiredPersonalisationLinks :many
-- Links whose window has closed and which nobody has closed out yet.
--
-- The sweep that reads this releases the jobs for printing, so it is the thing
-- that makes "expiry proceeds with Shopify's values" true. Bounded, because a
-- backlog after an outage must not become one enormous transaction.
SELECT * FROM personalisation_links
WHERE status IN ('sent', 'opened')
  AND expires_at <= now()
ORDER BY expires_at
LIMIT sqlc.arg('row_limit');

-- name: ExpirePersonalisationLink :exec
UPDATE personalisation_links
SET status = 'expired'
WHERE id = sqlc.arg('id');

-- name: ListPersonalisationLinks :many
-- The brand's links, newest first, for a staff-facing log.
SELECT * FROM personalisation_links
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY created_at DESC
LIMIT sqlc.arg('row_limit');

-- name: EnsurePersonalisationWatermark :one
-- Returns when this brand started asking customers, setting it to now on the
-- first ever pass.
--
-- ON CONFLICT DO UPDATE with a no-op SET rather than DO NOTHING: DO NOTHING
-- returns no row when one exists, so the caller would have to SELECT again and
-- handle a race it cannot win. This always returns the authoritative mark in
-- one round trip, and never moves an existing one.
INSERT INTO personalisation_watermarks (brand_slug, watch_from)
VALUES (sqlc.arg('brand_slug'), now())
ON CONFLICT (brand_slug) DO UPDATE SET brand_slug = excluded.brand_slug
RETURNING *;

-- name: ResetPersonalisationWatermark :exec
-- Starts asking from now, ignoring every order placed before.
INSERT INTO personalisation_watermarks (brand_slug, watch_from)
VALUES (sqlc.arg('brand_slug'), now())
ON CONFLICT (brand_slug) DO UPDATE SET watch_from = now();

-- name: GetOpenPersonalisationLinkForJob :one
-- The link whose window is open right now for this job's order.
--
-- Asked by the renderer the moment a model lands, to decide whether the job
-- goes straight to validated as it always has, or waits for the customer.
-- No row - no link, a send that failed, a window already closed - means the
-- old behaviour, so a brand with this switched off, or a customer with no
-- phone number, is never held up by a check that cannot happen.
SELECT l.* FROM personalisation_links l
JOIN production_jobs j ON j.order_id = l.order_id
WHERE j.id = sqlc.arg('job_id')
  AND l.status IN ('sent', 'opened')
  AND l.expires_at > now();

-- name: GetOrderBrandSlug :one
-- Which brand an order belongs to.
--
-- Through the connection it was imported on, because that is the only link an
-- order has to a brand - orders carry no brand_slug of their own, and the
-- customer's check needs one to resolve that brand's WhatsApp credentials and
-- its watermark.
SELECT c.brand_slug
FROM orders o
JOIN brand_connections c ON c.id = o.shop_connection_id
WHERE o.id = sqlc.arg('order_id');
