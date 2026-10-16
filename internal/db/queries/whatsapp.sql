-- name: FindWhatsAppBrandsByPhoneNumberID :many
-- The brand(s) whose WhatsApp integration sends from this number. The id is
-- not secret, so it can be matched in SQL.
SELECT brand_slug FROM integration_settings
WHERE provider = 'whatsapp' AND setting_key = 'phone_number_id' AND setting_value = $1;

-- name: ListWhatsAppBrands :many
SELECT DISTINCT brand_slug FROM integration_settings WHERE provider = 'whatsapp';

-- name: EnsureWhatsAppConversation :exec
INSERT INTO whatsapp_conversations (brand_slug, wa_id) VALUES ($1, $2)
ON CONFLICT (brand_slug, wa_id) DO NOTHING;

-- name: LockWhatsAppConversation :one
SELECT * FROM whatsapp_conversations WHERE brand_slug = $1 AND wa_id = $2 FOR UPDATE;

-- name: GetWhatsAppConversation :one
SELECT * FROM whatsapp_conversations WHERE brand_slug = $1 AND wa_id = $2;

-- name: SaveWhatsAppConversation :exec
UPDATE whatsapp_conversations
SET state = $3, context = $4, last_customer_at = $5, last_staff_at = $6,
    human_since = $7, opted_out = $8, updated_at = now()
WHERE brand_slug = $1 AND wa_id = $2;

-- name: TouchWhatsAppConversation :exec
-- Keeps the inbox's chat list current: the latest message, when the customer
-- last wrote (which opens WhatsApp's 24-hour reply window whether or not the
-- bot is on) and, when Meta sent one, the customer's profile name.
UPDATE whatsapp_conversations
SET last_customer_at = CASE WHEN sqlc.arg(from_customer)::boolean
                            THEN GREATEST(COALESCE(last_customer_at, sqlc.arg(at)), sqlc.arg(at))
                            ELSE last_customer_at END,
    last_message_at = GREATEST(COALESCE(last_message_at, sqlc.arg(at)), sqlc.arg(at)),
    last_message_preview = CASE WHEN last_message_at IS NULL OR sqlc.arg(at) >= last_message_at
                                THEN sqlc.arg(preview) ELSE last_message_preview END,
    profile_name = COALESCE(NULLIF(sqlc.arg(profile_name)::text, ''), profile_name),
    updated_at = now()
WHERE brand_slug = sqlc.arg(brand_slug) AND wa_id = sqlc.arg(wa_id);

-- name: ListWhatsAppConversations :many
SELECT * FROM whatsapp_conversations
WHERE brand_slug = $1 AND last_message_at IS NOT NULL
ORDER BY last_message_at DESC
LIMIT sqlc.arg(row_limit);

-- name: InsertWhatsAppMessage :execrows
-- media_status is set by the caller: 'pending' when there is an id to fetch,
-- '' otherwise. Nothing else ever writes it to 'pending', so the download
-- worker's set of work is exactly the messages that arrived with an
-- attachment.
INSERT INTO whatsapp_messages (
    brand_slug, wa_id, wa_message_id, direction, kind, body, status, sent_by, created_at,
    media_id, media_mime_type, media_sha256, media_status
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
    sqlc.arg('media_id'), sqlc.arg('media_mime_type'),
    sqlc.arg('media_sha256'), sqlc.arg('media_status'))
ON CONFLICT (wa_message_id) DO NOTHING;

-- name: SetWhatsAppMessageMedia :exec
-- The download landed: point the message at the stored file.
--
-- Guarded on 'pending' so a replayed job cannot overwrite a file that is
-- already there with a second copy of the same bytes.
UPDATE whatsapp_messages
SET file_asset_id   = sqlc.arg('file_asset_id'),
    media_mime_type = coalesce(nullif(sqlc.arg('media_mime_type')::text, ''), media_mime_type),
    media_status    = 'stored',
    media_error     = ''
WHERE wa_message_id = sqlc.arg('wa_message_id') AND media_status = 'pending';

-- name: FailWhatsAppMessageMedia :exec
-- It is never coming. Recorded rather than left pending for ever, so a screen
-- can say "unavailable" instead of "still downloading" until the heat death.
UPDATE whatsapp_messages
SET media_status = 'failed', media_error = sqlc.arg('media_error')
WHERE wa_message_id = sqlc.arg('wa_message_id') AND media_status = 'pending';

-- name: UpdateWhatsAppMessageStatus :exec
UPDATE whatsapp_messages SET status = $2, error_code = $3 WHERE wa_message_id = $1;

-- name: ListWhatsAppMessages :many
SELECT * FROM whatsapp_messages
WHERE brand_slug = $1 AND wa_id = $2
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: PurgeWhatsAppMessages :execrows
-- Drops old chat, EXCEPT anything a damage report was built from.
--
-- damage_report_photos cascades on message_id, so an unconditional purge
-- would quietly strip a resolved report's photographs at a hundred and eighty
-- days while leaving the file_asset rows and the S3 objects orphaned. A
-- report is a record somebody may need long after the small talk around it
-- has gone.
DELETE FROM whatsapp_messages m
WHERE m.created_at < $1
  AND NOT EXISTS (
      SELECT 1 FROM damage_report_photos p WHERE p.message_id = m.id
  );

-- name: StartWhatsAppConfirmations :one
-- The floor for confirmations: set the first time the sweep sees them on.
INSERT INTO whatsapp_brand_state (brand_slug, confirmations_since) VALUES ($1, now())
ON CONFLICT (brand_slug) DO UPDATE SET brand_slug = EXCLUDED.brand_slug
RETURNING confirmations_since;

-- name: StopWhatsAppConfirmations :exec
DELETE FROM whatsapp_brand_state WHERE brand_slug = $1;

-- name: ListOrdersAwaitingConfirmation :many
-- Paid orders of this brand placed since confirmations were switched on, with
-- no confirmation yet. Orders carry no brand column; the order-number prefix
-- is what ties an order to a brand.
SELECT o.* FROM orders o
WHERE o.order_number LIKE sqlc.arg(order_prefix)::text || '%'
  AND o.financial_status = 'paid'
  AND o.placed_at >= sqlc.arg(since)
  AND NOT EXISTS (SELECT 1 FROM order_confirmations c WHERE c.order_id = o.id)
ORDER BY o.placed_at
LIMIT sqlc.arg(row_limit);

-- name: InsertOrderConfirmation :execrows
INSERT INTO order_confirmations (order_id, brand_slug, order_number, shopify_gid, wa_id, first_name, summary, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (order_id) DO NOTHING;

-- name: GetOrderConfirmation :one
SELECT * FROM order_confirmations WHERE order_id = $1;

-- name: GetOrderConfirmationByGID :one
SELECT * FROM order_confirmations WHERE shopify_gid = $1;

-- name: GetOrderConfirmationByMessageID :one
SELECT * FROM order_confirmations WHERE wa_message_id = $1;

-- name: MarkOrderConfirmationSent :exec
UPDATE order_confirmations
SET status = $2, wa_message_id = $3, sent_at = COALESCE(sent_at, now()), updated_at = now()
WHERE order_id = $1;

-- name: TransitionOrderConfirmation :execrows
-- Moves a confirmation only from one of the expected statuses, so a late job
-- never overwrites the customer's own answer.
UPDATE order_confirmations
SET status = sqlc.arg(status),
    change_text = COALESCE(sqlc.narg(change_text), change_text),
    responded_at = CASE WHEN sqlc.arg(customer_reply)::boolean THEN now() ELSE responded_at END,
    updated_at = now()
WHERE order_id = sqlc.arg(order_id)
  AND status = ANY(sqlc.arg(from_statuses)::text[]);

-- name: GetWhatsAppMessageByWAID :one
SELECT * FROM whatsapp_messages WHERE wa_message_id = $1;

-- name: GetWhatsAppMessageMedia :one
-- One message's attachment, scoped to the brand whose conversation it is on.
--
-- THE BRAND IS IN THE WHERE CLAUSE, not checked afterwards. This is what makes
-- the route safe to expose by message id: a file belonging to one brand's chat
-- cannot be fetched under another brand's slug, which the flat /files/:id
-- route cannot promise.
SELECT m.id, m.brand_slug, m.kind, m.media_status, m.media_mime_type, m.media_error,
       f.storage_key, f.content_type, f.size_bytes, f.filename
FROM whatsapp_messages m
LEFT JOIN file_assets f ON f.id = m.file_asset_id
WHERE m.id = sqlc.arg('id') AND m.brand_slug = sqlc.arg('brand_slug');
