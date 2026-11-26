-- name: ListProductPhotoSizes :many
SELECT * FROM product_photo_sizes WHERE product_id = sqlc.arg('product_id') ORDER BY position, created_at;

-- name: DeleteProductPhotoSizes :exec
DELETE FROM product_photo_sizes WHERE product_id = sqlc.arg('product_id');

-- name: InsertProductPhotoSize :exec
INSERT INTO product_photo_sizes (id, product_id, width_mm, height_mm, shape, when_field, when_value, position, updated_by)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('width_mm'), sqlc.arg('height_mm'), sqlc.arg('shape'),
        sqlc.arg('when_field'), sqlc.arg('when_value'), sqlc.arg('position'), sqlc.arg('updated_by'));

-- name: PhotoSizesForSKU :many
-- The photo sizes of the product an order line's SKU belongs to, matched on
-- the same terms as FindProductBySKU.
SELECT s.* FROM product_photo_sizes s
JOIN product_variants v ON v.product_id = s.product_id
JOIN products p ON p.id = s.product_id
WHERE lower(v.sku) = lower(sqlc.arg('sku'))
  AND v.status = 'active'
  AND p.status = 'active'
ORDER BY s.position, s.created_at;

-- name: GetProductPhotoSample :one
SELECT * FROM product_photo_samples WHERE product_id = sqlc.arg('product_id');

-- name: UpsertProductPhotoSample :one
INSERT INTO product_photo_samples (product_id, filename, storage_key, size_bytes, uploaded_by)
VALUES (sqlc.arg('product_id'), sqlc.arg('filename'), sqlc.arg('storage_key'), sqlc.arg('size_bytes'), sqlc.arg('uploaded_by'))
ON CONFLICT (product_id) DO UPDATE SET
    filename = EXCLUDED.filename, storage_key = EXCLUDED.storage_key,
    size_bytes = EXCLUDED.size_bytes, uploaded_by = EXCLUDED.uploaded_by, created_at = now()
RETURNING *;

-- name: DeleteProductPhotoSample :exec
DELETE FROM product_photo_samples WHERE product_id = sqlc.arg('product_id');
