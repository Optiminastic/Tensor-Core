-- +goose Up
-- A third outcome, and one fewer state on the way to a reverse pickup.
--
-- CLOSED is support settling a report in conversation: a missing screw posted
-- on, a mark that buffs out. Nothing is made, sent or collected. It was not
-- storable before, and 0113 says why in as many words - "a third kind is a
-- code change, and it should fail here rather than be quietly storable". It
-- is now that code change, so the set grows by one.
ALTER TABLE damage_reports
    DROP CONSTRAINT IF EXISTS ck_damage_resolution_kind;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_resolution_kind
        CHECK (resolution_kind IN ('', 'reprint', 'reverse', 'closed'));

-- 'agreed' goes, because there is nothing left to agree to.
--
-- The reverse ask used to be two messages: an offer with Yes/No buttons, then
-- the QR once the customer tapped Yes. The shop asked for them combined, so
-- the ask, the QR and the request for a screenshot are now one message and
-- PAYING IS AGREEING. A customer who does not want a reverse pickup simply
-- does not pay.
--
-- Any report sitting in 'agreed' is moved to 'offered' FIRST, before the
-- constraint could refuse it. There are none today - the flow was never
-- driven, every damage report is still '' - but a migration that assumed
-- that would fail on the one database where it was not true, which is the
-- one that matters.
UPDATE damage_reports SET reverse_state = 'offered' WHERE reverse_state = 'agreed';

ALTER TABLE damage_reports
    DROP CONSTRAINT IF EXISTS ck_damage_reverse_state;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_reverse_state
        CHECK (reverse_state IN ('', 'offered', 'payment_claimed', 'paid', 'pickup_booked'));

-- +goose Down
ALTER TABLE damage_reports DROP CONSTRAINT IF EXISTS ck_damage_reverse_state;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_reverse_state
        CHECK (reverse_state IN ('', 'offered', 'agreed', 'payment_claimed', 'paid', 'pickup_booked'));
ALTER TABLE damage_reports DROP CONSTRAINT IF EXISTS ck_damage_resolution_kind;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_resolution_kind
        CHECK (resolution_kind IN ('', 'reprint', 'reverse'));
