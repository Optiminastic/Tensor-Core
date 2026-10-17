-- name: ListWhatsAppBotContent :many
-- One brand's overrides, in the order they are shown.
SELECT * FROM whatsapp_bot_content
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY kind, position, content_key;

-- name: UpsertWhatsAppBotContent :one
INSERT INTO whatsapp_bot_content (
    brand_slug, kind, content_key, title, description, answer, position, enabled, updated_by
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('kind'), sqlc.arg('content_key'),
    sqlc.arg('title'), sqlc.arg('description'), sqlc.arg('answer'),
    sqlc.arg('position'), sqlc.arg('enabled'), sqlc.narg('updated_by')
)
ON CONFLICT (brand_slug, kind, content_key) DO UPDATE SET
    title = EXCLUDED.title, description = EXCLUDED.description,
    answer = EXCLUDED.answer, position = EXCLUDED.position,
    enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteWhatsAppBotContent :execrows
-- Removes an override, which restores whatever the code says for that key.
DELETE FROM whatsapp_bot_content
WHERE brand_slug = sqlc.arg('brand_slug')
  AND kind = sqlc.arg('kind')
  AND content_key = sqlc.arg('content_key');
