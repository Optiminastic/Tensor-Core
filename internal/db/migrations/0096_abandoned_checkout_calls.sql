-- +goose Up
-- Renumbered from 0093. The number matters more than it looks.
--
-- These two were written before 0095_integration_settings and pushed after
-- it, which puts them BELOW the version every deployed database is already
-- at. goose.Up refuses that: it collects every on-disk migration below the
-- recorded max version that has not been applied and returns
--
--   error: found 2 missing migrations before current version 95
--
-- applying nothing. internal/db/migrate.go calls goose.Up with no options, so
-- there is no WithAllowMissing to soften it, and cmd/api migrates on boot -
-- the API would simply not start. Verified against goose v3.27.2's
-- findMissingMigrations, which only looks at files on disk, so the reverse
-- case (a recorded version whose file is gone) is harmless.
--
-- One row per abandoned checkout the win-back agent has rung, or tried to.
--
-- THE POINT OF THIS TABLE IS NOT TO CALL TWICE. The scheduler re-reads
-- Shopify's abandoned list every few minutes, and the same checkout is in every
-- read for as long as Shopify keeps it. Without a record of what has already
-- been dialled, a customer who left a basket this morning gets a phone call on
-- every tick for the next thirty days - which is not a bug anybody would catch
-- from a log line, but a person being harassed.
--
-- THE KEY IS THE PHONE NUMBER, NOT THE CHECKOUT. One call per person, ever.
-- A customer who abandons three carts in a fortnight is one person, and
-- deduplicating per checkout would ring them three times while every row
-- looked correct in isolation.
--
-- The unique index is the enforcement, not the SELECT above it: two scheduler
-- replicas can read the same list at the same moment and both decide to call.
-- The database is the only place that decision can be made once.
--
-- A failed attempt is recorded too, with its reason. The most likely failure
-- today is Sarvam answering 402 (no credit), and a row saying so is what tells
-- an operator why the shop's carts went unrung - rather than silence, which
-- looks identical to "nobody abandoned anything".
CREATE TABLE IF NOT EXISTS abandoned_checkout_calls (
    id              uuid PRIMARY KEY,
    brand_slug      varchar(64)  NOT NULL,
    -- Shopify's gid. The stable key; `name` is for people.
    checkout_id     varchar(128) NOT NULL,
    checkout_name   varchar(64)  NOT NULL,
    customer_name   varchar(120) NOT NULL DEFAULT '',
    -- E.164. Kept so an operator can see WHO was rung without going back to
    -- Shopify for a checkout it may since have dropped.
    phone           varchar(24)  NOT NULL,
    -- Sarvam's correlation key, null when the call never got placed.
    attempt_id      varchar(128),
    -- Sarvam's interaction id, which is what its transcript and recording are
    -- addressed by. Learned AFTER the call from the analytics API, never at
    -- dial time, so it is null until the log page has resolved it once.
    interaction_id  varchar(160),
    -- 'placed' | 'failed' | 'skipped'
    status          varchar(16)  NOT NULL,
    -- Sarvam's own sentence on a refusal. Its errors name the thing to fix.
    detail          varchar(500),
    attempts        integer      NOT NULL DEFAULT 1,
    last_attempt_at timestamptz  NOT NULL DEFAULT now(),
    created_at      timestamptz  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_abandoned_checkout_call_phone
    ON abandoned_checkout_calls (phone);

-- Not unique: the same person's later checkouts are recorded as skipped
-- against their own id, so the log can say WHY a cart was never rung.
CREATE INDEX IF NOT EXISTS ix_abandoned_checkout_calls_checkout
    ON abandoned_checkout_calls (checkout_id);

-- The scheduler's own read: "which of these have I already handled?"
CREATE INDEX IF NOT EXISTS ix_abandoned_checkout_calls_brand
    ON abandoned_checkout_calls (brand_slug, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS abandoned_checkout_calls;
