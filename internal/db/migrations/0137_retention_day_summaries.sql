-- +goose Up
-- What survives a day being deleted.
--
-- The shop keeps thirty days of orders, jobs and batches and removes the rest.
-- Removing them without keeping anything would mean October simply never
-- happened: no count of what was sold, nothing to check a bank statement
-- against, and no way to answer "how many did we print that week" six months
-- later. So every day is rolled up BEFORE it is deleted, and the rollup stays.
--
-- ONE ROW PER BRAND PER DAY, keyed so a re-run cannot double-count. The
-- retention pass is idempotent by construction: summarise, delete, and a
-- second pass over the same day finds nothing left to delete and overwrites
-- an identical summary.
--
-- THE DAY IS THE SHOP'S DAY, noon to noon IST, matching the accounts register
-- rather than the calendar - the floor works past midnight, and a calendar day
-- would split one evening's trading across two rows that neither the register
-- nor this table could reconcile.
--
-- This is a SUMMARY, not an archive. It cannot reproduce an invoice or tell
-- you who bought what. The daily sales register - one spreadsheet per day,
-- mailed and stored - is the record that can, and is the reason deleting the
-- rows is survivable at all.
CREATE TABLE IF NOT EXISTS retention_day_summaries (
    id             uuid PRIMARY KEY,
    brand_slug     varchar(64) NOT NULL,
    -- The window's closing noon, so a summary lines up with the register that
    -- covers the same trading day.
    window_end     timestamptz NOT NULL,
    orders         integer NOT NULL DEFAULT 0,
    line_items     integer NOT NULL DEFAULT 0,
    -- Kept as the sum of the orders' totals, in their own currency. Not a
    -- substitute for the register: a figure to sanity-check it against.
    revenue        numeric(14, 2) NOT NULL DEFAULT 0,
    currency       varchar(8) NOT NULL DEFAULT 'INR',
    jobs_completed integer NOT NULL DEFAULT 0,
    jobs_failed    integer NOT NULL DEFAULT 0,
    units          integer NOT NULL DEFAULT 0,
    batches        integer NOT NULL DEFAULT 0,
    -- When the rows behind this summary were removed, and how many went.
    deleted_at     timestamptz,
    deleted_orders integer NOT NULL DEFAULT 0,
    deleted_jobs   integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_slug, window_end)
);

CREATE INDEX IF NOT EXISTS ix_retention_summaries_brand
    ON retention_day_summaries (brand_slug, window_end DESC);

-- The retention sweep walks orders by age, so it needs that order cheaply.
CREATE INDEX IF NOT EXISTS ix_orders_created_at
    ON orders (created_at);

-- +goose Down
DROP TABLE IF EXISTS retention_day_summaries;
