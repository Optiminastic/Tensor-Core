-- name: GetActiveTemplate :one
-- The uploaded source for one template key, if somebody has replaced it.
--
-- Asked on every render, so it is a single indexed lookup rather than a join:
-- the renderer needs the storage key and nothing else, and a plank render
-- already costs 22 seconds without adding a wide row to it.
SELECT t.*, f.storage_key, f.filename
FROM design_templates t
JOIN file_assets f ON f.id = t.file_id
WHERE lower(t.template_key) = lower(sqlc.arg('template_key')::text)
  AND t.status = 'active';

-- name: ListActiveTemplates :many
-- Every key somebody has uploaded a file for, for the Designs tab.
SELECT t.*, f.storage_key, f.filename, f.size_bytes
FROM design_templates t
JOIN file_assets f ON f.id = t.file_id
WHERE t.status = 'active'
ORDER BY lower(t.template_key);

-- name: ListTemplateHistory :many
-- Every version of one key, newest first. What answers "which file printed the
-- planks we shipped last Tuesday".
SELECT t.*, f.storage_key, f.filename, f.size_bytes
FROM design_templates t
JOIN file_assets f ON f.id = t.file_id
WHERE lower(t.template_key) = lower(sqlc.arg('template_key')::text)
ORDER BY t.version DESC;

-- name: SupersedeTemplate :exec
-- Retires whatever was active for a key, so the new upload can take its place.
--
-- Superseded rather than deleted: the file it points at is what printed real
-- planks, and losing that record loses the answer to what a customer received.
UPDATE design_templates SET status = 'superseded'
WHERE lower(template_key) = lower(sqlc.arg('template_key')::text) AND status = 'active';

-- name: NextTemplateVersion :one
-- One past the highest version this key has ever had, so numbers never repeat
-- even after a row is superseded.
SELECT coalesce(max(version), 0) + 1 AS next_version
FROM design_templates
WHERE lower(template_key) = lower(sqlc.arg('template_key')::text);

-- name: InsertTemplate :one
INSERT INTO design_templates (id, template_key, file_id, version, status, uploaded_by, notes)
VALUES (sqlc.arg('id'), sqlc.arg('template_key'), sqlc.arg('file_id'),
        sqlc.arg('version'), 'active', sqlc.arg('uploaded_by'), sqlc.narg('notes'))
RETURNING *;
