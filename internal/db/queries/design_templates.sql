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

-- name: ListProductsUsingTemplate :many
-- Which products link a key, so deleting one can say what would break.
--
-- Active designs only: a superseded design belongs to a variant that has since
-- been pointed somewhere else, and refusing a delete over it would make a key
-- undeletable forever on the strength of a link nothing reads.
SELECT DISTINCT p.code, d.role
FROM variant_designs d
JOIN product_variants v ON v.id = d.variant_id
JOIN products p ON p.id = v.product_id
WHERE d.status = 'active'
  AND lower(d.template_key) = lower(sqlc.arg('template_key')::text)
ORDER BY p.code, d.role;

-- name: RetireTemplate :execrows
-- Removes a key from the registry without losing what it printed.
--
-- RETIRED, NOT DELETED, and for the same reason SupersedeTemplate is: the file
-- a retired key points at is what printed real orders, and "which file made
-- the thing this customer is holding" has to stay answerable after somebody
-- tidies up. The row leaves ListActiveTemplates, so the key disappears from
-- the Designs tab; its history does not move.
--
-- Uploading the key again later makes a new active version, because
-- NextTemplateVersion counts every row rather than the active one.
UPDATE design_templates SET status = 'retired'
WHERE lower(template_key) = lower(sqlc.arg('template_key')::text) AND status = 'active';

-- name: ListTemplateSections :many
-- Every filed label, for the Designs tab. A handful of rows; read whole.
SELECT template_key, section FROM design_template_sections;

-- name: SetTemplateSection :exec
-- Files a design file under a heading, or moves it to another one.
--
-- Upsert rather than insert-or-update at the call site: the dropdown sends the
-- whole answer every time, and a key that has never been filed is the ordinary
-- case rather than an error.
INSERT INTO design_template_sections (template_key, section, updated_by, updated_at)
VALUES (lower(sqlc.arg('template_key')::text), sqlc.arg('section'), sqlc.arg('updated_by'), now())
ON CONFLICT (template_key) DO UPDATE
SET section = EXCLUDED.section, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ClearTemplateSection :exec
-- Unfiles a design file, so it falls back to the heading its use implies.
DELETE FROM design_template_sections
WHERE template_key = lower(sqlc.arg('template_key')::text);
