-- +goose Up
-- The win-back ledger learns WHICH CHANNEL each contact was made on.
--
-- The claim was channel-blind: one unique index on (phone), one row per
-- person for all time. That was right while the only channel was a phone
-- call. The moment a WhatsApp message fires at the same T+10 trigger, the two
-- channels race for that single row and whichever claims first silently
-- cancels the other - the call takes the row, the message's INSERT hits
-- ON CONFLICT DO NOTHING, returns nothing, and the sweep stands down exactly
-- as though a replica had beaten it. No error, no log line, no message. Every
-- sweep, forever.
--
-- 'voice' as the default backfills every existing row, which is what every
-- existing row is.
ALTER TABLE abandoned_checkout_calls
    ADD COLUMN IF NOT EXISTS channel varchar(16) NOT NULL DEFAULT 'voice';

-- THE BRAND IS IN THE KEY NOW, because the read already filtered by it and
-- the index did not.
--
-- ListCalledPhones asks "which numbers has THIS BRAND been through?" while
-- the index enforced "which numbers has ANYBODY been through?". On a
-- multi-brand install those disagree: a candidate passes every rule, the
-- INSERT hits the global index, DO NOTHING returns no row, and the sweep
-- stands down silently. The index and the query now ask the same question.
--
-- The consequence, stated plainly: two brands on one Tensor may each contact
-- the same person once. That is the behaviour the per-brand read always
-- implied; it is now also the behaviour the database enforces.
DROP INDEX IF EXISTS uq_abandoned_checkout_call_phone;

CREATE UNIQUE INDEX IF NOT EXISTS uq_winback_contact_brand_phone_channel
    ON abandoned_checkout_calls (brand_slug, phone, channel);

-- The sweep's own read is per brand; the page's is per brand and channel.
CREATE INDEX IF NOT EXISTS ix_abandoned_checkout_calls_channel
    ON abandoned_checkout_calls (brand_slug, channel);

-- +goose Down
-- BEST EFFORT, and it will refuse on a table that has used both channels:
-- recreating a global unique index over rows that legitimately repeat a phone
-- number is not something a migration can resolve on its own.
DROP INDEX IF EXISTS ix_abandoned_checkout_calls_channel;
DROP INDEX IF EXISTS uq_winback_contact_brand_phone_channel;
CREATE UNIQUE INDEX IF NOT EXISTS uq_abandoned_checkout_call_phone
    ON abandoned_checkout_calls (phone);
ALTER TABLE abandoned_checkout_calls DROP COLUMN IF EXISTS channel;
