-- +goose Up
-- One damage report per completed WhatsApp damage flow.
--
-- Before this, a customer reporting damage left Tensor nothing. The bot read
-- the order and the photo count out of the conversation's scratchpad, wrote a
-- tag and a note to SHOPIFY, and then handed the chat to a human - which
-- clears that scratchpad. Afterwards nothing in Tensor's own database said
-- which order had been damaged, how many photographs arrived, or whether
-- anybody had dealt with it. "Who reported damage this week?" had no answer.
--
-- THE ORDER IS SHOPIFY'S, NOT TENSOR'S. The bot resolves it live against the
-- Shopify Admin API, so the GID and the number are always known while
-- orders.id may not be: the order might not be imported yet, or at all. Hence
-- shopify_order_gid NOT NULL and order_id nullable, and the queue reads the
-- number off this row rather than joining to find it.
CREATE TABLE IF NOT EXISTS damage_reports (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_slug        varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    wa_id             varchar(32) NOT NULL,
    shopify_order_gid varchar(64) NOT NULL,
    order_number      varchar(64) NOT NULL,
    -- SET NULL, not CASCADE: a report outliving its order row is fine, losing
    -- the report is not.
    order_id          uuid REFERENCES orders(id) ON DELETE SET NULL,
    -- Stored rather than joined. Shopify is the source and this row should
    -- still read correctly years later, whatever happens to the order.
    customer_name     varchar(255) NOT NULL DEFAULT '',
    photo_count       integer NOT NULL DEFAULT 0,
    -- new -> in_review -> resolved, and any status may move to any other: a
    -- report reopened after a refund is a real thing, and a one-way ladder
    -- would need a fourth status to say so.
    status            varchar(16) NOT NULL DEFAULT 'new',
    resolution_note   text NOT NULL DEFAULT '',
    -- The wamid of the message that closed the flow. UNIQUE, so a replayed
    -- inbound job cannot file the same report twice.
    wa_message_id     varchar(128) NOT NULL UNIQUE,
    reported_at       timestamptz NOT NULL DEFAULT now(),
    status_changed_at timestamptz,
    -- A Better Auth user id, with no foreign key - the house style for actor
    -- columns, since Better Auth owns that table.
    status_changed_by varchar(64),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_damage_reports_queue
    ON damage_reports (brand_slug, status, reported_at DESC);
CREATE INDEX IF NOT EXISTS ix_damage_reports_order
    ON damage_reports (order_id) WHERE order_id IS NOT NULL;

-- The photographs, BY THE MESSAGE THEY ARRIVED ON, never by file id.
--
-- The download is asynchronous: when the customer types DONE their photographs
-- may still be in flight, so there is no file to point at yet. Linking to the
-- message means the link is complete the instant the report is written, and
-- the media worker goes on knowing nothing about damage reports - it only ever
-- fills in whatsapp_messages.file_asset_id. Reading through the message is
-- also what lets a screen say "1 of 3 received" instead of guessing, and the
-- caption travels with the photograph for free, because the caption IS that
-- message's body.
CREATE TABLE IF NOT EXISTS damage_report_photos (
    report_id  uuid NOT NULL REFERENCES damage_reports(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES whatsapp_messages(id) ON DELETE CASCADE,
    position   integer NOT NULL,
    PRIMARY KEY (report_id, message_id)
);

-- +goose Down
DROP TABLE IF EXISTS damage_report_photos;
DROP TABLE IF EXISTS damage_reports;
