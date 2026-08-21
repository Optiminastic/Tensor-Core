-- File assets (uploaded models, photos, generated plates). Numeric bbox columns
-- cast to float8 so handlers work in float64; nullable bboxes stay pointers.

-- name: InsertFileAsset :one
INSERT INTO file_assets (
    id, filename, content_type, size_bytes, storage_key, is_template, uploaded_by,
    bbox_x_mm, bbox_y_mm, bbox_z_mm
) VALUES (
    sqlc.arg('id'), sqlc.arg('filename'), sqlc.arg('content_type'), sqlc.arg('size_bytes'),
    sqlc.arg('storage_key'), sqlc.arg('is_template'), sqlc.arg('uploaded_by'),
    sqlc.narg('bbox_x_mm')::float8, sqlc.narg('bbox_y_mm')::float8, sqlc.narg('bbox_z_mm')::float8
)
RETURNING id, filename, content_type, size_bytes, storage_key, is_template, uploaded_by,
          bbox_x_mm, bbox_y_mm, bbox_z_mm, created_at;

-- name: GetFileAsset :one
SELECT id, filename, content_type, size_bytes, storage_key, is_template, uploaded_by,
       bbox_x_mm, bbox_y_mm, bbox_z_mm, created_at
FROM file_assets WHERE id = $1;

-- name: UpdateFileAssetBBox :exec
-- Backfills a file asset's measured dimensions. Needed for template files minted
-- from a design's model, which are recorded without ever being measured.
UPDATE file_assets SET
    bbox_x_mm = sqlc.narg('bbox_x_mm')::float8,
    bbox_y_mm = sqlc.narg('bbox_y_mm')::float8,
    bbox_z_mm = sqlc.narg('bbox_z_mm')::float8
WHERE id = sqlc.arg('id');
