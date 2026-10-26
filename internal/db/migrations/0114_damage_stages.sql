-- +goose Up
-- Every step a damaged order passes through, and what happened at it.
--
-- A damage report used to carry a status and a resolution: enough to say "this
-- is being dealt with", not enough to say WHAT HAS ACTUALLY HAPPENED. A
-- reprint is four separate acts against three separate systems - book the
-- courier, tell the customer, re-queue the print, watch it dispatch - and any
-- of them can fail on its own while the others succeeded. Collapsed into one
-- status, a report whose courier was booked but whose customer was never told
-- is indistinguishable from one where both worked.
--
-- So each step is a ROW, written as it happens, and the report's own stage
-- column is the furthest one reached. The drawer reads this table as a
-- timeline; the board reads the column.
--
-- APPEND-ONLY. A failed attempt stays in the table when a retry succeeds,
-- because "the courier refused us twice before accepting" is the thing
-- somebody wants to know in three months, and an UPDATE would erase it.
CREATE TABLE IF NOT EXISTS damage_stages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id  uuid NOT NULL REFERENCES damage_reports(id) ON DELETE CASCADE,

    -- Which step. Named rather than numbered: the order differs between a
    -- reprint and a reverse, and a number would imply one sequence.
    stage      varchar(32) NOT NULL,

    -- done | failed | skipped
    --
    -- 'skipped' is not a failure and is recorded rather than left absent: a
    -- reverse pickup books no forward courier, and a timeline that simply
    -- omitted the step would read as one still waiting to happen.
    state      varchar(16) NOT NULL,

    -- What happened, for a person: a waybill number, a Delhivery refusal, the
    -- reason WhatsApp would not take the message. Shown in the drawer, so it
    -- is written for the operator rather than for a log parser.
    detail     text NOT NULL DEFAULT '',

    -- Who, when a person did it. Null for a step the system took by itself.
    actor      varchar(64),
    at         timestamptz NOT NULL DEFAULT now()
);

-- The drawer's query: this report's steps, oldest first.
CREATE INDEX IF NOT EXISTS ix_damage_stages_report
    ON damage_stages (report_id, at);

-- The furthest step this report has reached, denormalised onto the report so
-- the BOARD does not need a join per row.
--
-- Deliberately a copy. The table is the record; this is what the list column
-- reads, and it is written in the same transaction as the row it reflects.
ALTER TABLE damage_reports
    ADD COLUMN IF NOT EXISTS stage varchar(32) NOT NULL DEFAULT 'reported';

-- Existing reports predate the stage log. They are back-filled from what they
-- already record, so the board does not show a week of live reports as though
-- nothing had happened to them.
UPDATE damage_reports
SET stage = CASE
    WHEN reverse_state = 'pickup_booked' THEN 'dispatched'
    WHEN reverse_state = 'paid'            THEN 'payment_confirmed'
    WHEN reverse_state = 'payment_claimed' THEN 'payment_claimed'
    WHEN reverse_state = 'agreed'          THEN 'customer_agreed'
    WHEN reverse_state = 'offered'         THEN 'customer_told'
    WHEN resolution_kind <> ''             THEN 'decided'
    ELSE 'reported'
END
WHERE stage = 'reported';

-- +goose Down
ALTER TABLE damage_reports DROP COLUMN IF EXISTS stage;
DROP INDEX IF EXISTS ix_damage_stages_report;
DROP TABLE IF EXISTS damage_stages;
