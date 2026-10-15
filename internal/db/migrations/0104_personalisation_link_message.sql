-- +goose Up
-- The message as it was sent, frozen beside the fact that it was sent.
--
-- The Personalisation tab rebuilt the message on every read: it took the
-- template name from THIS process's configuration and fetched today's copy
-- from Meta. That is exact only while every process agrees on the name, and
-- they did not. tensor-api carries no PERSONALISATION_TEMPLATE_NAME, so the
-- rebuild asked for "" - and whatsapp.Client.Template falls back to the
-- configured default, which for this brand is the cart-recovery template. The
-- tab therefore quoted the abandoned-checkout message to an operator asking
-- what a customer had been told about their order. Measured in production on
-- 15 Oct 2026.
--
-- The win-back ledger already learned this (see 0099): what was said is a
-- fact about a moment, not a thing to re-derive later. These two columns are
-- what was composed, and they do not change afterwards.
--
-- THE LINK IS NOT STORED. The win-back keeps its button URL; this one must
-- not, because the token in it is a live credential - token_sealed exists
-- precisely so the raw link is kept encrypted and only while the window is
-- open. The body is the words, and the words are what the tab needs.
--
-- Nullable: every row written before this migration has no frozen copy, and
-- the tab falls back to rebuilding those by explicit name.
ALTER TABLE personalisation_links
    -- The template actually used for THIS send, not whatever the reader is
    -- configured with.
    ADD COLUMN IF NOT EXISTS template_name varchar(160),
    -- The approved body with this order's own values in it, as the customer
    -- read it - asterisks and emoji included.
    ADD COLUMN IF NOT EXISTS message_body text,
    -- How many buttons sat under it. Frozen with the rest so reading back a
    -- sent message needs nothing from Meta at all: a template that gains or
    -- loses a button later must not change what last week's message is said
    -- to have looked like. Not the labels - Meta's API hands back a URL
    -- button's url and not its text, and an invented label is worse than a
    -- count.
    ADD COLUMN IF NOT EXISTS button_count smallint;

-- +goose Down
ALTER TABLE personalisation_links DROP COLUMN IF EXISTS button_count;
ALTER TABLE personalisation_links DROP COLUMN IF EXISTS message_body;
ALTER TABLE personalisation_links DROP COLUMN IF EXISTS template_name;
