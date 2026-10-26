-- name: ListWhatsAppBotNodes :many
-- Every message in this brand's tree, in display order.
SELECT * FROM whatsapp_bot_nodes
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY position, node_key;

-- name: ListWhatsAppBotOptions :many
-- Every option across the whole tree, in order within each node.
--
-- One query rather than one per node: the bot resolves the entire tree once
-- per turn, and a tree of five nodes must not be five round trips on the
-- path that answers a customer.
SELECT * FROM whatsapp_bot_options
WHERE brand_slug = sqlc.arg('brand_slug')
ORDER BY node_key, position;

-- name: UpsertWhatsAppBotNode :exec
INSERT INTO whatsapp_bot_nodes (brand_slug, node_key, kind, body, list_button, position)
VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('node_key'), sqlc.arg('kind'),
    sqlc.arg('body'), sqlc.arg('list_button'), sqlc.arg('position')
)
ON CONFLICT (brand_slug, node_key) DO UPDATE
SET kind        = excluded.kind,
    body        = excluded.body,
    list_button = excluded.list_button,
    position    = excluded.position,
    updated_at  = now();

-- name: UpsertWhatsAppBotOption :exec
INSERT INTO whatsapp_bot_options (
    brand_slug, node_key, option_key, title, description,
    action, answer, target, button_label, position, enabled, updated_by
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('node_key'), sqlc.arg('option_key'),
    sqlc.arg('title'), sqlc.arg('description'), sqlc.arg('action'),
    sqlc.arg('answer'), sqlc.arg('target'), sqlc.arg('button_label'),
    sqlc.arg('position'), sqlc.arg('enabled'), sqlc.narg('updated_by')
)
ON CONFLICT (brand_slug, node_key, option_key) DO UPDATE
SET title        = excluded.title,
    description  = excluded.description,
    action       = excluded.action,
    answer       = excluded.answer,
    target       = excluded.target,
    button_label = excluded.button_label,
    position     = excluded.position,
    enabled      = excluded.enabled,
    updated_by   = excluded.updated_by,
    updated_at   = now();

-- name: DeleteWhatsAppBotTree :exec
-- Clears the brand's whole tree.
--
-- THE SAVE IS A REPLACE, not a patch. Which nodes exist, which options are
-- on them and the order of both are one decision, so the page sends the
-- whole tree and this clears what was there first. Options cascade from
-- their node, so one statement is enough.
DELETE FROM whatsapp_bot_nodes WHERE brand_slug = sqlc.arg('brand_slug');

-- name: ListBotMessages :many
-- This brand's rewritten one-off messages.
--
-- READ ON EVERY SEND, deliberately. The table holds a handful of rows behind
-- its primary key, and the alternative - a cache - is how an edit saved on the
-- Bot Messages page keeps sending the old wording until somebody restarts the
-- API. "Changes apply to the next message" is the promise the page makes.
SELECT message_key, body FROM whatsapp_bot_messages
WHERE brand_slug = sqlc.arg('brand_slug');

-- name: UpsertBotMessage :exec
-- Stores one rewritten message, replacing whatever was there.
INSERT INTO whatsapp_bot_messages (brand_slug, message_key, body, updated_by)
VALUES (sqlc.arg('brand_slug'), sqlc.arg('message_key'), sqlc.arg('body'), sqlc.narg('updated_by'))
ON CONFLICT (brand_slug, message_key) DO UPDATE
SET body       = excluded.body,
    updated_by = excluded.updated_by,
    updated_at = now();

-- name: DeleteBotMessage :exec
-- Puts one message back to the wording in the binary.
--
-- A DELETE, NOT A COPY OF THE DEFAULT. Storing the default as a row would
-- freeze it: a later improvement to the shipped wording would never reach a
-- brand that had once pressed "reset".
DELETE FROM whatsapp_bot_messages
WHERE brand_slug = sqlc.arg('brand_slug') AND message_key = sqlc.arg('message_key');
