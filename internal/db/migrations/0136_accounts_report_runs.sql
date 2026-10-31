-- +goose Up
-- Every register that has gone out, and every one that failed trying.
--
-- accounts_report_settings carries LAST_sent_at, last_row_count and
-- last_error - one row per brand, overwritten each day. That answers "did
-- today's go?" and nothing else: an accountant asking for Tuesday's file, or
-- anybody asking why Thursday's never arrived, had nowhere to look. The
-- settings columns stay as they are, because the worker's due-check reads
-- last_window_end and that idempotency is not worth disturbing.
--
-- THE SPREADSHEET IS KEPT, not just a record that one was sent. file_id
-- points at the same file_assets row every other stored artefact uses, so the
-- same download route serves it and the same object storage holds it. A run
-- whose file could not be stored still records the send - the mail went, and
-- a missing attachment here must not read as a missing report.
CREATE TABLE IF NOT EXISTS accounts_report_runs (
    id            uuid PRIMARY KEY,
    brand_slug    varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    -- The noon-to-noon window this register covers, not when it was mailed:
    -- a run retried after a failure covers the same day it always did.
    window_start  timestamptz NOT NULL,
    window_end    timestamptz NOT NULL,
    sent_at       timestamptz NOT NULL DEFAULT now(),
    -- 'sent' or 'failed'. A failure is a row too, which is the point.
    status        varchar(16) NOT NULL,
    detail        text,
    row_count     integer     NOT NULL DEFAULT 0,
    order_count   integer     NOT NULL DEFAULT 0,
    -- Who it went to, AS SENT. Reading today's settings to describe last
    -- week's mail would show the wrong recipients the moment somebody edits
    -- the list - which is exactly when you are looking.
    to_addresses  text[]      NOT NULL DEFAULT '{}',
    cc_addresses  text[]      NOT NULL DEFAULT '{}',
    bcc_addresses text[]      NOT NULL DEFAULT '{}',
    file_id       uuid        REFERENCES file_assets(id) ON DELETE SET NULL,
    -- 'scheduled' or 'manual', so a test send is not mistaken for the day's.
    source        varchar(16) NOT NULL DEFAULT 'scheduled',
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- The page reads one brand newest-first, which is the only access pattern.
CREATE INDEX IF NOT EXISTS ix_accounts_report_runs_brand
    ON accounts_report_runs (brand_slug, window_end DESC);

-- +goose Down
DROP TABLE IF EXISTS accounts_report_runs;
