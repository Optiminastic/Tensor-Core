-- Product creation page: finding one order to make print artwork from.

-- name: FindOrderByNumberForBrand :one
-- By the number an operator types: "T3DPS-116597" or just "116597". The
-- TRAILING digits are compared, not all of them - the shop's own prefix
-- T3DPS carries a 3, and "all digits" made T3DPS-116597 read 3116597. The
-- brand keeps two storefronts' 116597s apart.
SELECT * FROM orders
WHERE brand_slug = sqlc.arg('brand_slug')
  AND (lower(order_number) = lower(sqlc.arg('number')::text)
       OR (sqlc.arg('number')::text ~ '\d$'
           AND substring(order_number from '(\d+)$') = substring(sqlc.arg('number')::text from '(\d+)$')))
ORDER BY placed_at DESC NULLS LAST, imported_at DESC
LIMIT 1;

-- name: InsertProductCreationBuild :one
INSERT INTO product_creation_builds (id, brand_slug, order_number, product, variant, requested_by)
VALUES (sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('order_number'), sqlc.arg('product'),
        sqlc.arg('variant'), sqlc.arg('requested_by'))
RETURNING *;

-- name: FindReusableProductCreationBuild :one
-- The newest build of this file that is finished or still on its way - so a
-- second click downloads the first build rather than making another.
SELECT * FROM product_creation_builds
WHERE brand_slug = sqlc.arg('brand_slug')
  AND product = sqlc.arg('product')
  AND variant = sqlc.arg('variant')
  AND lower(order_number) = lower(sqlc.arg('order_number'))
  AND (status = 'done' OR (status IN ('queued', 'building') AND updated_at > now() - interval '15 minutes'))
ORDER BY created_at DESC
LIMIT 1;

-- name: GetProductCreationBuild :one
SELECT * FROM product_creation_builds WHERE id = sqlc.arg('id');

-- name: MarkProductCreationBuild :exec
UPDATE product_creation_builds
SET status      = sqlc.arg('status'),
    storage_key = COALESCE(sqlc.narg('storage_key'), storage_key),
    filename    = COALESCE(sqlc.narg('filename'), filename),
    error       = sqlc.narg('error'),
    updated_at  = now()
WHERE id = sqlc.arg('id');
