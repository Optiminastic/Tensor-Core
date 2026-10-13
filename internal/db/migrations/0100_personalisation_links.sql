-- +goose Up
-- The customer's own check on what they ordered, before anything prints.
--
-- Tensor has never asked the person whose name is being engraved whether it is
-- the right name. AutoValidatePersonalisation asks only "is each slot
-- non-empty?", which is why five planks once printed with the first line's
-- names and the operator reading JOB-115059-2 had no button to press. A link
-- goes out the moment the order arrives; the customer sees each item as a 3D
-- model, corrects it, and confirms.

-- One link per ORDER, and that is the whole deduplication story.
--
-- NOT per phone. The win-back ledger is keyed (brand_slug, phone, channel) -
-- one contact per person, for all time - which is right for a win-back and
-- catastrophic here: a returning customer's second order would hit the index,
-- the insert would return no row, and the send would stand down looking
-- exactly like a lost race. That is the failure 0098 was written to fix, and
-- it must not be reintroduced under a new name.
--
-- The unique order_id also makes the re-import safe. Every sync re-imports
-- every order back to the floor date and UpsertPaidOrder does not report
-- whether the row was new, so without this a routine sync would message the
-- same customer again on every pass.
CREATE TABLE IF NOT EXISTS personalisation_links (
    id          uuid PRIMARY KEY,
    brand_slug  varchar(64) NOT NULL,
    order_id    uuid NOT NULL UNIQUE REFERENCES orders(id) ON DELETE CASCADE,
    -- SHA-256 of a 32-byte random token. THE RAW TOKEN IS NEVER STORED, as
    -- with user_invites: it is handed to the sender once and exists after
    -- that only in the customer's own message. A database dump therefore
    -- opens nobody's link.
    token_hash  varchar(64) NOT NULL UNIQUE,
    -- Who it went to, in E.164, so the log can answer "did we message this
    -- person" without reading the order back.
    phone       varchar(24) NOT NULL DEFAULT '',
    -- 'pending' | 'sent' | 'opened' | 'confirmed' | 'expired' | 'failed'
    status      varchar(16) NOT NULL,

    -- THE COUNTDOWN RUNS FROM ISSUE, not from first open. One stored instant
    -- that both the expiry sweep and the timer on the page read, so the
    -- customer's clock and Tensor's cannot disagree - a page saying four
    -- minutes left while the sweep has already closed the window is worse
    -- than a shorter window.
    issued_at   timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    sent_at     timestamptz,
    opened_at   timestamptz,
    confirmed_at timestamptz,

    -- Meta's wamid, and the sender's own sentence when it refused.
    message_id  varchar(160),
    detail      varchar(500),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The sweep's two reads: what is still open for this brand, and what has run
-- out of time anywhere.
CREATE INDEX IF NOT EXISTS ix_personalisation_links_brand_status
    ON personalisation_links (brand_slug, status);
CREATE INDEX IF NOT EXISTS ix_personalisation_links_expiry
    ON personalisation_links (expires_at)
    WHERE status IN ('sent', 'opened');

-- When this brand started asking customers, so switching the feature on does
-- not message everyone who ordered in the last month.
--
-- Per brand, not global: stores are connected at different times, and a single
-- mark would silence a newly connected one. Same shape and same reasoning as
-- winback_watermarks.
CREATE TABLE IF NOT EXISTS personalisation_watermarks (
    brand_slug varchar(64) PRIMARY KEY,
    watch_from timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- What the order said before the customer touched it.
--
-- personalisation_properties is overwritten by an edit - it has to be, because
-- it is what the renderer reads - so without this the change is invisible the
-- instant it is made, and the job page could never answer "what did they
-- change?".
--
-- WRITTEN ONCE, on the first edit, and never again. A second edit must not
-- move it or the hover card would show the customer's own first correction as
-- though it were what they ordered.
--
-- NULL means nothing was ever edited. That is the Done case, it needs no
-- backfill for the rows that already exist, and it keeps "unchanged" and
-- "changed back to the original" distinguishable - the second is a customer
-- who looked, and the shop should be able to see that they did.
--
-- Carries the colour as well as the property list, because colour is not a
-- property: it lives in personalisation_colour and colours, and a snapshot of
-- the properties alone would quietly lose the one change most likely to be
-- made.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS personalisation_original jsonb;

-- +goose Down
ALTER TABLE production_jobs DROP COLUMN IF EXISTS personalisation_original;
DROP TABLE IF EXISTS personalisation_watermarks;
DROP TABLE IF EXISTS personalisation_links;
