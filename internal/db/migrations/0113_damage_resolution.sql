-- +goose Up
-- What the shop decided to do about a damaged parcel, and how far it has got.
--
-- A damage report could say new / in_review / resolved and carry a note. That
-- records that somebody dealt with it; it records nothing about WHICH of the
-- two things they did, and the two are not alike:
--
--   REPRINT - we make it again and send it, and the customer pays nothing.
--   REVERSE - the customer pays Rs 100 for the leg from their home to the
--             warehouse, we repair it, and we pay the leg back.
--
-- The reverse one is a negotiation with money in it, spread over days and
-- three people: the admin who offers it, the customer who agrees and pays, the
-- support person who confirms the payment, and the admin again who books the
-- pickup. Every one of those is a step that can be waiting, and "in_review"
-- cannot say which.
--
-- Hence reverse_state, which is the thing a person reads off the board to know
-- whose turn it is.
ALTER TABLE damage_reports
    -- '' until an admin decides. Not NULL-able: a report nobody has decided
    -- about and a report decided as neither are the same thing, and an empty
    -- string says it without a third case to handle on every read.
    ADD COLUMN IF NOT EXISTS resolution_kind varchar(16) NOT NULL DEFAULT '',

    -- The reverse negotiation, start to finish. Empty on a reprint and on an
    -- undecided report.
    --
    --   offered         the Rs 100 offer has gone to the customer
    --   agreed          they said yes; the QR has gone
    --   payment_claimed they sent a screenshot - NOBODY HAS CHECKED IT YET
    --   paid            support looked at the screenshot and confirmed it
    --   pickup_booked   an admin booked the reverse pickup with Delhivery
    --
    -- payment_claimed is deliberately its own state rather than folded into
    -- paid. A screenshot is a picture of a screen; it is not proof, and the
    -- whole reason support exists in this flow is that a person has to look.
    -- Collapsing the two would mean the board could not tell "they say they
    -- paid" from "we have the money".
    ADD COLUMN IF NOT EXISTS reverse_state varchar(24) NOT NULL DEFAULT '',

    -- WHEN each step happened and WHO did it, for the three steps a person
    -- takes. Money changes hands here, so "support marked this paid" has to
    -- stay answerable months later - the same reason status_changed_by exists.
    ADD COLUMN IF NOT EXISTS offered_at timestamptz,
    ADD COLUMN IF NOT EXISTS agreed_at timestamptz,
    ADD COLUMN IF NOT EXISTS payment_claimed_at timestamptz,
    ADD COLUMN IF NOT EXISTS payment_confirmed_at timestamptz,
    ADD COLUMN IF NOT EXISTS payment_confirmed_by varchar(64),
    ADD COLUMN IF NOT EXISTS pickup_booked_at timestamptz,
    ADD COLUMN IF NOT EXISTS pickup_booked_by varchar(64),

    -- The screenshot the customer sent, so support reads the report rather
    -- than hunting through a chat for it. Points at file_assets, where the
    -- WhatsApp media worker already puts every inbound attachment.
    ADD COLUMN IF NOT EXISTS payment_proof_file_id uuid REFERENCES file_assets(id) ON DELETE SET NULL,

    -- Delhivery's own number for the reverse pickup, once an admin books it.
    -- The link between this report and the parcel coming back.
    ADD COLUMN IF NOT EXISTS pickup_waybill varchar(40) NOT NULL DEFAULT '';

-- Only the two the code writes. A CHECK rather than a lookup table because
-- these are a closed set decided in Go (internal/damage), not shop data:
-- a third kind is a code change, and it should fail here rather than be
-- quietly storable.
ALTER TABLE damage_reports
    DROP CONSTRAINT IF EXISTS ck_damage_resolution_kind;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_resolution_kind
        CHECK (resolution_kind IN ('', 'reprint', 'reverse'));

ALTER TABLE damage_reports
    DROP CONSTRAINT IF EXISTS ck_damage_reverse_state;
ALTER TABLE damage_reports
    ADD CONSTRAINT ck_damage_reverse_state
        CHECK (reverse_state IN ('', 'offered', 'agreed', 'payment_claimed', 'paid', 'pickup_booked'));

-- A reverse report that is waiting on somebody is what the board is for, so
-- the index covers the question it asks: this brand's reverse reports, by
-- state, newest first.
CREATE INDEX IF NOT EXISTS ix_damage_reports_reverse
    ON damage_reports (brand_slug, reverse_state, reported_at DESC)
    WHERE resolution_kind = 'reverse';

-- +goose Down
DROP INDEX IF EXISTS ix_damage_reports_reverse;
ALTER TABLE damage_reports
    DROP CONSTRAINT IF EXISTS ck_damage_reverse_state,
    DROP CONSTRAINT IF EXISTS ck_damage_resolution_kind,
    DROP COLUMN IF EXISTS pickup_waybill,
    DROP COLUMN IF EXISTS payment_proof_file_id,
    DROP COLUMN IF EXISTS pickup_booked_by,
    DROP COLUMN IF EXISTS pickup_booked_at,
    DROP COLUMN IF EXISTS payment_confirmed_by,
    DROP COLUMN IF EXISTS payment_confirmed_at,
    DROP COLUMN IF EXISTS payment_claimed_at,
    DROP COLUMN IF EXISTS agreed_at,
    DROP COLUMN IF EXISTS offered_at,
    DROP COLUMN IF EXISTS reverse_state,
    DROP COLUMN IF EXISTS resolution_kind;
