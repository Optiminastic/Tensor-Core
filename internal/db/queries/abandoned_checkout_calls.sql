-- name: ClaimWinbackContact :one
-- Takes the right to reach this PERSON on this CHANNEL, or reports that it is
-- already taken.
--
-- Inserted BEFORE anything is sent, not after. A row written afterwards leaves
-- a window in which the process can die with the customer's phone ringing and
-- nothing recorded - and the next sweep rings them again. Claiming first means
-- a crash costs a missed contact, which is recoverable, instead of a repeated
-- one, which is not.
--
-- PER CHANNEL, because a customer who was rung is still due a message. A
-- single row for both would mean whichever channel claimed first silently
-- cancelled the other, which is indistinguishable from losing a race to
-- another replica. Per brand too - see migration 0098 for why that was wrong
-- before.
--
-- DO NOTHING on conflict, so a second replica gets no row back and stands down.
INSERT INTO abandoned_checkout_calls (
    id, brand_slug, checkout_id, checkout_name, customer_name,
    phone, channel, status
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('checkout_id'),
    sqlc.arg('checkout_name'), sqlc.arg('customer_name'),
    sqlc.arg('phone'), sqlc.arg('channel'), 'claimed'
)
ON CONFLICT (brand_slug, phone, channel) DO NOTHING
RETURNING *;

-- name: SettleWinbackContact :one
-- Records how the claimed contact actually went.
--
-- attempts is incremented rather than set, so a row that was retried says so.
UPDATE abandoned_checkout_calls
SET status          = sqlc.arg('status'),
    attempt_id      = coalesce(sqlc.narg('attempt_id'), attempt_id),
    detail          = sqlc.narg('detail'),
    attempts        = attempts + 1,
    last_attempt_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ReleaseWinbackContact :exec
-- Gives the number back, for a claim whose message or call never left.
--
-- ONLY FOR A TRANSIENT REFUSAL - no credit, rate limiting, the provider down.
-- A PERMANENT one is settled as 'failed' and KEPT: the row is the only thing
-- that can tell an operator why a shop's carts went uncontacted, and silence
-- there is indistinguishable from "nobody abandoned anything". Guarded on a
-- null attempt_id, which is the proof that nothing was sent.
DELETE FROM abandoned_checkout_calls
WHERE id = sqlc.arg('id') AND attempt_id IS NULL;

-- name: ListWinbackContacts :many
-- Every number this brand has been through, on every channel, in one read.
--
-- One query for both passes rather than one per channel: it is the same
-- question asked twice, and the sweep already costs a Shopify page.
SELECT phone, channel, status, attempt_id, last_attempt_at
FROM abandoned_checkout_calls
WHERE brand_slug = sqlc.arg('brand_slug');

-- name: ListCheckoutCalls :many
-- What the agent has done, newest first - the Outreach page's own read.
--
-- Optionally filtered by channel. Meta's message id and Sarvam's attempt id
-- both live in attempt_id, so a page that does not filter will ask Sarvam for
-- the transcript of a WhatsApp message.
SELECT * FROM abandoned_checkout_calls
WHERE brand_slug = sqlc.arg('brand_slug')
  AND (sqlc.narg('channel')::varchar IS NULL OR channel = sqlc.narg('channel'))
ORDER BY created_at DESC
LIMIT sqlc.arg('row_limit');

-- name: SetCallInteractionID :exec
-- Caches the interaction id once the analytics API has told us, so the
-- transcript can be fetched later without listing attempts again.
--
-- Voice only. A WhatsApp row's attempt_id is a wamid and Sarvam knows nothing
-- about it; the formats never collide, but saying so here is cheaper than
-- relying on that.
UPDATE abandoned_checkout_calls
SET interaction_id = sqlc.arg('interaction_id')
WHERE attempt_id = sqlc.arg('attempt_id') AND channel = 'voice';
