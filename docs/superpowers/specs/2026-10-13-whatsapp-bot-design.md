# WhatsApp Bot and Inbox in Tensor - Design

Status: approved in chat on 2026-10-13.
Origin: built first as a standalone service (`tech3-optiminastic/whatsapp-bot`), then moved into Tensor so it reuses Tensor's WhatsApp credentials, Shopify connection, orders and deployment.

## Goal

For a brand with WhatsApp connected (first: `the-3d-printing-store-india`, number +91 91139 94682):

1. **Menu bot.** Any customer message gets a list menu: Where is my order, Damage concern, Explore more, More (FAQ sub-list).
2. **Personalisation confirmation.** After a paid order is imported, the customer confirms the names to be printed, or asks for a change.
3. **Inbox.** Staff read every conversation and reply from Tensor. Replies from the WhatsApp Business app (coexistence) show up in the same thread.

## Reuse

| Need | Existing Tensor piece |
| --- | --- |
| Credentials | `integration_settings` provider `whatsapp` (per brand, secrets sealed by secretbox) |
| Sending | `internal/integrations/whatsapp` (extended: text, list, quick-reply template buttons, webhook parsing) |
| Shopify | `brand_connections` token + `internal/integrations/shopify` client (extended: order by name, tags, note) |
| Orders | `orders` table, filled every 10 minutes by the order sync; `line_items[].properties` hold every custom attribute |
| Jobs | River via `internal/workerset` (runs in `tensor-production-worker`; the API only enqueues) |

## New brand settings (WhatsApp integration)

| Key | Meaning |
| --- | --- |
| `app_secret` (exists) | Verifies `X-Hub-Signature-256` on the webhook. Required for the bot. |
| `webhook_verify_token` (secret) | Echoed in Meta's subscribe handshake. |
| `bot_enabled` | `true` turns the menu bot on. |
| `confirmations_enabled` | `true` turns personalisation confirmations on; the time it is first seen on is the floor, so old orders are never messaged. |
| `order_prefix` | Default `T3DPS-`. Order numbers customers type are matched as prefix + digits. |

## Flow

```
Meta --POST /integrations/whatsapp/webhook--> verify (brand app_secret) --> store message + enqueue (one tx)
                                                                            |
tensor-production-worker: River whatsapp_inbound (1 worker) --> whatsappbot.Step (pure) --> actions
  actions: send text/list (Cloud API), order lookup (Shopify live), tag/note order (Shopify), record confirmation
periodic whatsapp_confirmations sweep (5 min): new paid orders --> confirm template --> reminder 24h --> auto-confirm 48h
```

- The brand is found from `metadata.phone_number_id` (non-secret setting).
- The body is parsed once to find the brand, then the signature is checked with that brand's `app_secret`. No secret means 503, not open.
- One River worker on the inbound queue keeps a customer's messages in order; the conversation row is locked `FOR UPDATE` per job.
- Staff echoes (`smb_message_echoes`) and inbox replies put the conversation into human mode; the bot stays silent for 12 hours after the last human activity or until the customer types `MENU`.

## Data (migration 0100)

- `whatsapp_conversations`: `(brand_slug, wa_id)` PK, `profile_name`, `state`, `context` jsonb, `last_customer_at`, `last_staff_at`, `human_since`, `opted_out`, `last_message_at`, `last_message_preview`, timestamps.
- `whatsapp_messages`: `id`, `brand_slug`, `wa_id`, `wa_message_id` unique, `direction` (`in`, `out`, `echo`), `kind`, `body` text, `status`, `error_code`, `sent_by` (Better Auth user id for inbox replies), `created_at`. Purged after 180 days.
- `order_confirmations`: `order_id` uuid PK -> `orders`, `brand_slug`, `order_number`, `wa_id`, `first_name`, `summary`, `status`, `wa_message_id`, `change_text`, `sent_at`, `responded_at`, timestamps.

## API

| Route | Guard |
| --- | --- |
| `GET /integrations/whatsapp/webhook` (Meta handshake) | verify token, constant-time |
| `POST /integrations/whatsapp/webhook` | brand app_secret HMAC |
| `GET /brands/:slug/whatsapp/conversations` | `whatsapp:read` |
| `GET /brands/:slug/whatsapp/conversations/:wa_id/messages` | `whatsapp:read` |
| `POST /brands/:slug/whatsapp/conversations/:wa_id/messages` `{text}` | `whatsapp:reply`; 422 outside the 24h window |
| `POST /brands/:slug/whatsapp/conversations/:wa_id/handback` | `whatsapp:reply` |

New permissions `whatsapp:read` and `whatsapp:reply`, granted to ADMIN, PROJECT_LEAD and OPERATOR.

## Shopify tags written

`wa-confirmed`, `wa-auto-confirmed`, `wa-change-requested` (production must not print), `wa-unreachable`, `wa-damage-reported`.

## Frontend (Tensor repo, branch `feat/whatsapp-inbox`)

Agent section -> **WhatsApp Inbox** at `/[brand]/agent/inbox`: conversation list, thread of bubbles (customer, bot, staff), reply box, bot/human badge, hand-back button, refresh every 10 seconds.

## Testing

- Pure: the ported bot, summary, status, phone tests.
- Client: webhook parsing and signature, text/list/template request bodies.
- Integration (`TENSOR_TEST_DB`): webhook -> ingest -> worker -> sends, confirmation sweep lifecycle, inbox endpoints and guards.
