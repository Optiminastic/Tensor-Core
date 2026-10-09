-- name: EnsureWinbackWatermark :one
-- Returns when this brand started being watched, setting it to now on the
-- first ever sweep.
--
-- ON CONFLICT DO UPDATE with a no-op SET rather than DO NOTHING: DO NOTHING
-- returns no row when one already exists, so the caller would have to SELECT
-- again and handle a race it cannot win. This always returns the authoritative
-- mark in one round trip, and never moves an existing one.
INSERT INTO winback_watermarks (brand_slug, watch_from)
VALUES (sqlc.arg('brand_slug'), now())
ON CONFLICT (brand_slug) DO UPDATE SET brand_slug = excluded.brand_slug
RETURNING *;

-- name: ResetWinbackWatermark :exec
-- Starts watching again from now, ignoring everything abandoned before.
--
-- For an operator who wants a clean slate - after a long outage, say, where
-- the carts that piled up are no longer worth a call.
INSERT INTO winback_watermarks (brand_slug, watch_from)
VALUES (sqlc.arg('brand_slug'), now())
ON CONFLICT (brand_slug) DO UPDATE SET watch_from = now();
