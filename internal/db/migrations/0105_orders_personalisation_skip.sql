-- +goose Up
-- Why nobody was asked, written where somebody can read it.
--
-- The orders page draws a dash in the Personalisation column for an order
-- with no link row, and a dash is every refusal at once: the feature was off,
-- the order predates the watermark, nothing on it is personalised, there was
-- no usable phone, WhatsApp is not connected, the models were still
-- rendering. The worker names the rule on every one of those paths - and only
-- in a log line, which is gone the next time the container is recreated.
-- Asked "why is this one empty?", nobody could answer. Measured on 15 Oct
-- 2026, looking at T3DPS-115838.
--
-- So the reason lands on the order. ONE REASON, OVERWRITTEN, not a history:
-- the question is always "why is it like that now", and the send is retried
-- from scratch on every trigger, so an older reason is not evidence of
-- anything. The timestamp is what separates a refusal from this minute from
-- one recorded days ago under rules that have since changed.
--
-- Cleared the moment an order is claimed for a link, so a row can never both
-- carry a live window and a reason it was never asked.
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS personalisation_skip_reason text,
    ADD COLUMN IF NOT EXISTS personalisation_skipped_at  timestamptz;

-- +goose Down
ALTER TABLE orders DROP COLUMN IF EXISTS personalisation_skipped_at;
ALTER TABLE orders DROP COLUMN IF EXISTS personalisation_skip_reason;
