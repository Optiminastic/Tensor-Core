-- +goose Up
-- Damage reports by link, not by conversation.
--
-- The flow this replaces asked for the order number, looked it up live against
-- Shopify, asked for photographs, and waited for the customer to type DONE -
-- four turns before anybody had told us anything useful, with two unreadable
-- replies handing the chat to a person. Now tapping "Damage concern" mints a
-- link and the customer fills in a web form.
--
-- The link is the same shape as personalisation_links and for the same
-- reasons: the URL is the whole credential, so only its hash is stored and
-- every refusal is one refusal.
CREATE TABLE IF NOT EXISTS damage_links (
    id          uuid PRIMARY KEY,
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    -- Who it was sent to, in WhatsApp's own form. NOT an order: the whole
    -- point of the form is that the customer tells us which order it is, so
    -- the link is minted before anybody knows.
    wa_id       varchar(32) NOT NULL,
    -- SHA-256 of a 32-byte random token. THE RAW TOKEN IS NEVER STORED - it
    -- is handed to the sender once and exists after that only in the
    -- customer's own message.
    token_hash  varchar(64) NOT NULL UNIQUE,
    -- 'pending' | 'sent' | 'opened' | 'submitted' | 'expired' | 'failed'
    status      varchar(16) NOT NULL,
    issued_at   timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    sent_at     timestamptz,
    opened_at   timestamptz,
    submitted_at timestamptz,
    -- Meta's wamid, and the sender's own sentence when it refused.
    message_id  varchar(160),
    detail      varchar(500),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- ONE LIVE LINK PER PERSON. Tapping Damage twice must resend the link they
-- already have rather than mint a second one that orphans the first - a
-- customer who uploaded three photographs against the old token and then
-- tapped again would otherwise submit an empty report.
--
-- Partial, on the open statuses only, so a person who reports damage again
-- next month gets a fresh link.
CREATE UNIQUE INDEX IF NOT EXISTS ux_damage_links_live
    ON damage_links (brand_slug, wa_id)
    WHERE status IN ('sent', 'opened');

CREATE INDEX IF NOT EXISTS ix_damage_links_expiry
    ON damage_links (expires_at)
    WHERE status IN ('pending', 'sent', 'opened');

-- The files, BY THE LINK rather than by the report.
--
-- They are uploaded before the report exists - the customer picks photographs
-- and then presses Submit - so there is nothing else to hang them on at the
-- moment they arrive. The report reaches them through its own link_id.
--
-- This replaces damage_report_photos, which pointed at whatsapp_messages
-- because the WhatsApp download was asynchronous and there was no file to
-- point at yet. Nothing about that is true any more.
--
-- file_assets has NO brand_slug, so brand ownership runs through the link.
-- Never trust a file id handed in by the client.
CREATE TABLE IF NOT EXISTS damage_files (
    link_id    uuid NOT NULL REFERENCES damage_links(id) ON DELETE CASCADE,
    file_id    uuid NOT NULL REFERENCES file_assets(id) ON DELETE CASCADE,
    position   integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (link_id, file_id)
);

-- +goose StatementBegin
DO $$
BEGIN
    -- THE LINK IS THE NEW DEDUPE KEY, and the business rule with it: one
    -- report per link. A customer who reopens their link inside its 24 hours
    -- adds to the report they already filed rather than filing a second.
    ALTER TABLE damage_reports
        ADD COLUMN IF NOT EXISTS link_id uuid REFERENCES damage_links(id) ON DELETE SET NULL;

    -- Was UNIQUE and NOT NULL because a report was identified by the message
    -- that closed the flow. There is no such message now; a report filed from
    -- the form has no wamid at all.
    ALTER TABLE damage_reports DROP CONSTRAINT IF EXISTS damage_reports_wa_message_id_key;
    ALTER TABLE damage_reports ALTER COLUMN wa_message_id DROP NOT NULL;

    -- The form has no live Shopify lookup, so a report knows the order NUMBER
    -- the customer typed and often nothing else.
    ALTER TABLE damage_reports ALTER COLUMN shopify_order_gid SET DEFAULT '';
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS ux_damage_reports_link
    ON damage_reports (link_id) WHERE link_id IS NOT NULL;

DROP TABLE IF EXISTS damage_report_photos;

-- +goose Down
DROP INDEX IF EXISTS ux_damage_reports_link;
ALTER TABLE damage_reports DROP COLUMN IF EXISTS link_id;
DROP TABLE IF EXISTS damage_files;
DROP TABLE IF EXISTS damage_links;
CREATE TABLE IF NOT EXISTS damage_report_photos (
    report_id  uuid NOT NULL REFERENCES damage_reports(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES whatsapp_messages(id) ON DELETE CASCADE,
    position   integer NOT NULL,
    PRIMARY KEY (report_id, message_id)
);
