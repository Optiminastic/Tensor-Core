-- name: ClaimCustomerForCall :one
-- Takes the right to ring this PERSON, or reports that it is already taken.
--
-- Inserted BEFORE the call is placed, not after. A row written afterwards
-- leaves a window in which the process can die with the customer's phone
-- ringing and nothing recorded - and the next sweep rings them again. Claiming
-- first means a crash costs a missed call, which is recoverable, instead of a
-- repeated one, which is not.
--
-- Keyed on the phone, so a customer who abandons three carts is called once.
-- DO NOTHING on conflict, so a second replica gets no row back and stands down.
INSERT INTO abandoned_checkout_calls (
    id, brand_slug, checkout_id, checkout_name, customer_name, phone, status
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('checkout_id'),
    sqlc.arg('checkout_name'), sqlc.arg('customer_name'), sqlc.arg('phone'),
    'claimed'
)
ON CONFLICT (phone) DO NOTHING
RETURNING *;

-- name: SettleCheckoutCall :one
-- Records how the claimed call actually went.
UPDATE abandoned_checkout_calls
SET status          = sqlc.arg('status'),
    attempt_id      = coalesce(sqlc.narg('attempt_id'), attempt_id),
    detail          = sqlc.narg('detail'),
    last_attempt_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ReleaseCheckoutCall :exec
-- Gives the number back, for a claim whose call never reached the network.
--
-- The cost of claiming first: a refusal before dialling - no credit, a bad
-- number, Sarvam down - would otherwise mark a customer called forever on a
-- call that never happened. Only ever used when Sarvam returned no attempt id,
-- which is the proof that nothing rang.
DELETE FROM abandoned_checkout_calls
WHERE id = sqlc.arg('id') AND attempt_id IS NULL;

-- name: ListCalledPhones :many
-- Every number this brand has already been through, so a sweep can skip them
-- without a round trip per row.
SELECT phone, status, attempt_id, last_attempt_at
FROM abandoned_checkout_calls
WHERE brand_slug = sqlc.arg('brand_slug');

-- name: ListCheckoutCalls :many
-- What the agent has done, newest first - the Call Logs page's own read.
SELECT * FROM abandoned_checkout_calls
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY created_at DESC
LIMIT sqlc.arg('row_limit');

-- name: SetCallInteractionID :exec
-- Caches the interaction id once the analytics API has told us, so the
-- transcript can be fetched later without listing attempts again.
UPDATE abandoned_checkout_calls
SET interaction_id = sqlc.arg('interaction_id')
WHERE attempt_id = sqlc.arg('attempt_id');
