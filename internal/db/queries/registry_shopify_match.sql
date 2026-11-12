-- Matching an order line that carries no SKU to its registry product (0142).

-- name: SetProductShopifyID :exec
UPDATE products SET shopify_product_id = sqlc.arg('shopify_product_id'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: FindStandInSKUForShopifyLine :one
-- The stand-in SKU of the registry product an SKU-less order line belongs to:
-- the variant whose SKU is the product's own code, which an import creates for
-- a product with variants that carry none.
--
-- By Shopify product id first - stable across renames - and by the product's
-- name only when no product records that id, so a product imported before 0142
-- still matches until it is re-imported.
SELECT v.sku::text AS sku
FROM products p
JOIN product_variants v ON v.product_id = p.id
                       AND lower(v.sku) = lower(p.code)
                       AND v.status = 'active'
WHERE p.status = 'active'
  AND (
        (sqlc.arg('shopify_product_id')::text <> '' AND p.shopify_product_id = sqlc.arg('shopify_product_id')::text)
     OR (sqlc.arg('product_name')::text <> '' AND lower(p.name) = lower(sqlc.arg('product_name')::text)
         AND NOT EXISTS (SELECT 1 FROM products q
                         WHERE sqlc.arg('shopify_product_id')::text <> ''
                           AND q.shopify_product_id = sqlc.arg('shopify_product_id')::text))
      )
ORDER BY (p.shopify_product_id = sqlc.arg('shopify_product_id')::text) DESC NULLS LAST, p.created_at
LIMIT 1;
