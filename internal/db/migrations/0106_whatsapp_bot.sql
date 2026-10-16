-- +goose Up
-- Renumbered from 0100 on merge: 0100 was already taken by
-- 0100_personalisation_links.sql, which shipped first and is applied in
-- production. Two files at one version is not a style question - goose keys
-- its ledger on the number, so the second one is silently never applied and
-- every table below would be missing at runtime with no error anywhere.
-- The WhatsApp menu bot, the personalisation confirmation and the inbox.
--
-- One conversation per (brand, customer number). The bot's state machine is
-- pure; this row is where its state lives between messages, and the inbox reads
-- the same row for its chat list.
CREATE TABLE IF NOT EXISTS whatsapp_conversations (
    brand_slug           varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    wa_id                varchar(32) NOT NULL,
    profile_name         varchar(255) NOT NULL DEFAULT '',
    state                varchar(32) NOT NULL DEFAULT 'idle',
    context              jsonb NOT NULL DEFAULT '{}',
    last_customer_at     timestamptz,
    last_staff_at        timestamptz,
    human_since          timestamptz,
    opted_out            boolean NOT NULL DEFAULT false,
    last_message_at      timestamptz,
    last_message_preview text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, wa_id)
);
CREATE INDEX IF NOT EXISTS ix_whatsapp_conversations_recent
    ON whatsapp_conversations (brand_slug, last_message_at DESC);

-- Every message in either direction. direction is 'in' (customer), 'out'
-- (bot or an inbox reply; sent_by says which) or 'echo' (staff, from the
-- WhatsApp Business app). wa_message_id is Meta's wamid and dedupes webhook
-- retries. Holds personal data: purged after 180 days.
CREATE TABLE IF NOT EXISTS whatsapp_messages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_slug    varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    wa_id         varchar(32) NOT NULL,
    wa_message_id varchar(128) NOT NULL UNIQUE,
    direction     varchar(8) NOT NULL,
    kind          varchar(32) NOT NULL,
    body          text NOT NULL DEFAULT '',
    status        varchar(16) NOT NULL DEFAULT '',
    error_code    integer,
    sent_by       varchar(64),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_whatsapp_messages_thread
    ON whatsapp_messages (brand_slug, wa_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_whatsapp_messages_created_at
    ON whatsapp_messages (created_at);

-- One personalisation confirmation per order. status moves pending -> sent ->
-- reminded -> confirmed | change_requested | auto_confirmed | unreachable, and
-- the outcome is written to Shopify as an order tag.
CREATE TABLE IF NOT EXISTS order_confirmations (
    order_id      uuid PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    brand_slug    varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    order_number  varchar(64) NOT NULL,
    shopify_gid   varchar(64) NOT NULL,
    wa_id         varchar(32) NOT NULL,
    first_name    varchar(255) NOT NULL DEFAULT '',
    summary       text NOT NULL,
    status        varchar(24) NOT NULL DEFAULT 'pending',
    wa_message_id varchar(128),
    change_text   text,
    sent_at       timestamptz,
    responded_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_order_confirmations_wa_message_id
    ON order_confirmations (wa_message_id);

-- When confirmations were switched on for a brand. Orders placed before it are
-- never messaged, so turning the feature on does not text every past customer.
-- Its own row rather than the setting's updated_at, which moves every time an
-- admin saves the integration form.
CREATE TABLE IF NOT EXISTS whatsapp_brand_state (
    brand_slug          varchar(64) PRIMARY KEY REFERENCES brands(slug) ON DELETE CASCADE,
    confirmations_since timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS whatsapp_brand_state;
DROP TABLE IF EXISTS order_confirmations;
DROP TABLE IF EXISTS whatsapp_messages;
DROP TABLE IF EXISTS whatsapp_conversations;
