-- name: ListDesignFonts :many
SELECT * FROM design_fonts ORDER BY families[1], style, created_at;

-- name: GetDesignFontBySHA :one
SELECT * FROM design_fonts WHERE sha256 = sqlc.arg('sha256');

-- name: DesignFontsForFamilies :many
-- The uploaded faces a template naming any of these families needs.
-- Families are passed lower-cased, as they are stored.
SELECT * FROM design_fonts WHERE families && sqlc.arg('families')::text[] ORDER BY created_at;

-- name: InsertDesignFont :one
INSERT INTO design_fonts (id, families, style, filename, storage_key, sha256, size_bytes, uploaded_by)
VALUES (sqlc.arg('id'), sqlc.arg('families'), sqlc.arg('style'), sqlc.arg('filename'),
        sqlc.arg('storage_key'), sqlc.arg('sha256'), sqlc.arg('size_bytes'), sqlc.arg('uploaded_by'))
RETURNING *;
