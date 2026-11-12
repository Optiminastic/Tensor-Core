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
