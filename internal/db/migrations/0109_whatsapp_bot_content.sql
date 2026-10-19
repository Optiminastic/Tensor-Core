-- +goose Up
-- The words the bot says, editable without a deploy.
--
-- Every line the bot speaks was a Go constant, so changing "5 to 10 working
-- days" to "4 to 8" meant a developer, a build and a deploy - for a sentence
-- the shop rewrites more often than the code around it. The product registry
-- solved the same problem the same way (see internal/httpapi/registry.go).
--
-- AN OVERLAY, NOT A REPLACEMENT. An empty table means the bot behaves exactly
-- as it does today: the Go constants remain the default and a row here
-- overrides one of them. That is what makes this safe to ship - a brand that
-- has never opened the page is unaffected, and a bad edit is undone by
-- clearing the field rather than by a deploy.
--
-- kind says what a row is:
--   menu  - one of the three main-menu buttons. Only the LABEL is editable;
--           the action behind it is code, because "start the damage flow" is
--           a conversation with state, lookups and photographs, not a string.
--   faq   - one row of the list behind "Other", with its answer. These are
--           wholly data: an id nothing in the code knows about still works,
--           because the worker answers it from this table.
CREATE TABLE IF NOT EXISTS whatsapp_bot_content (
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    kind        varchar(16) NOT NULL,
    -- The id WhatsApp hands back when the customer taps. For a menu row it
    -- matches a code constant; for an FAQ row it can be anything unique.
    content_key varchar(64) NOT NULL,
    -- Button label, or list-row title. WhatsApp caps these at 20 and 24
    -- characters respectively and refuses the WHOLE message over one that is
    -- too long, naming neither the row nor the rule - so the API checks them.
    title       varchar(64) NOT NULL DEFAULT '',
    -- The grey line under a list row. Unused by menu rows.
    description varchar(96) NOT NULL DEFAULT '',
    -- What the customer is sent when they tap it. Unused by menu rows, whose
    -- behaviour is code.
    answer      text NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    -- Off rather than deleted, so a seasonal answer can come back without
    -- being retyped.
    enabled     boolean NOT NULL DEFAULT true,
    updated_by  varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, kind, content_key)
);

CREATE INDEX IF NOT EXISTS ix_whatsapp_bot_content_order
    ON whatsapp_bot_content (brand_slug, kind, position);

-- +goose Down
DROP TABLE IF EXISTS whatsapp_bot_content;
