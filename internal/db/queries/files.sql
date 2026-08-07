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

-- name: GetFileAssetByStorageKey :one
-- Used by the job-creation worker to check whether a design's STL already has
-- a file_assets row (with its bbox already computed) before downloading and
-- computing one - storage_key has no unique constraint, so this takes the
-- most recently created match.
SELECT id, filename, content_type, size_bytes, storage_key, is_template, uploaded_by,
       bbox_x_mm, bbox_y_mm, bbox_z_mm, created_at
FROM file_assets WHERE storage_key = $1 ORDER BY created_at DESC LIMIT 1;
