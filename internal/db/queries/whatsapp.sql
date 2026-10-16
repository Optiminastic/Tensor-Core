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
INSERT INTO whatsapp_messages (brand_slug, wa_id, wa_message_id, direction, kind, body, status, sent_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (wa_message_id) DO NOTHING;

-- name: UpdateWhatsAppMessageStatus :exec
UPDATE whatsapp_messages SET status = $2, error_code = $3 WHERE wa_message_id = $1;

-- name: ListWhatsAppMessages :many
SELECT * FROM whatsapp_messages
WHERE brand_slug = $1 AND wa_id = $2
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: PurgeWhatsAppMessages :execrows
DELETE FROM whatsapp_messages WHERE created_at < $1;

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
