-- name: GetAccountsReportSettings :one
-- One brand's report configuration, or nothing when it has never been set up.
--
-- The absence of a row is meaningful and is NOT defaulted here: a brand
-- nobody has configured must not inherit somebody else's accountant.
SELECT * FROM accounts_report_settings WHERE brand_slug = sqlc.arg('brand_slug');

-- name: UpsertAccountsReportSettings :one
-- Saves the whole form in one write.
--
-- The send history columns are deliberately NOT touched: this is the settings
-- screen, and editing a recipient must never look like the report was sent.
INSERT INTO accounts_report_settings (
    brand_slug, enabled, to_addresses, cc_addresses, bcc_addresses,
    gst_percent, seller_state, updated_by, updated_at
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('enabled'),
    sqlc.arg('to_addresses'), sqlc.arg('cc_addresses'), sqlc.arg('bcc_addresses'),
    sqlc.arg('gst_percent'), sqlc.arg('seller_state'),
    sqlc.arg('updated_by'), now()
)
ON CONFLICT (brand_slug) DO UPDATE SET
    enabled       = EXCLUDED.enabled,
    to_addresses  = EXCLUDED.to_addresses,
    cc_addresses  = EXCLUDED.cc_addresses,
    bcc_addresses = EXCLUDED.bcc_addresses,
    gst_percent   = EXCLUDED.gst_percent,
    seller_state  = EXCLUDED.seller_state,
    updated_by    = EXCLUDED.updated_by,
    updated_at    = now()
RETURNING *;

-- name: ListDueAccountsReports :many
-- Brands whose window has closed and who have not been sent it yet.
--
-- THE IDEMPOTENCY IS HERE, not in the worker's clock. The worker wakes often
-- inside one noon-to-noon window; last_window_end is what stops a restart, a
-- second process or a manual run from mailing the accountant the same day
-- twice. A brand that has never been sent anything has a null here and is due
-- as soon as it is enabled.
--
-- Switched-off brands are excluded in SQL rather than in Go: a disabled brand
-- is not "due and skipped", it is not due, and counting it as skipped work
-- makes the worker's own log read as if it were doing something.
SELECT * FROM accounts_report_settings
WHERE enabled = true
  AND cardinality(to_addresses) > 0
  AND (last_window_end IS NULL OR last_window_end < sqlc.arg('window_end'))
ORDER BY brand_slug;

-- name: RecordAccountsReportSent :exec
-- Marks a window closed and clears the last failure.
--
-- last_window_end is written even when the day had no orders: a quiet day is
-- a sent report, and leaving it null would re-send every wake-up.
UPDATE accounts_report_settings
SET last_window_end = sqlc.arg('window_end'),
    last_sent_at    = now(),
    last_row_count  = sqlc.arg('row_count'),
    last_error      = NULL
WHERE brand_slug = sqlc.arg('brand_slug');

-- name: RecordAccountsReportFailure :exec
-- Records why a send failed, WITHOUT advancing the window.
--
-- Not advancing is the point: the next wake-up tries the same day again. A
-- failure that moved the window on would lose that day's register silently,
-- and nobody would know until the accountant asked.
UPDATE accounts_report_settings
SET last_error = sqlc.arg('last_error')
WHERE brand_slug = sqlc.arg('brand_slug');

-- The window's orders are NOT read from here.
--
-- There was a ListOrdersForAccountsWindow query, and it was wrong in a way
-- worth recording: orders.line_items stores production.LineItem, which carries
-- product_name, sku, quantity and the personalisation fields and NO PRICE -
-- the pipeline's line item describes what to print, not what was charged.
-- A register built from it went out with every money column 0.00.
--
-- The order's own total is there, but apportioning one total across lines that
-- cost different amounts invents figures, and a sales register is the last
-- place to do that. So the figures come from Shopify, which has them per line
-- and after discount. See accountsOrdersInWindow.

-- name: InsertAccountsReportRun :one
-- Records one register, sent or failed. A failure is a row too: "nothing
-- arrived on Thursday" is a question the page has to be able to answer, and
-- it cannot from an absence.
INSERT INTO accounts_report_runs (
    id, brand_slug, window_start, window_end, status, detail,
    row_count, order_count, to_addresses, cc_addresses, bcc_addresses,
    file_id, source
) VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('window_start'),
    sqlc.arg('window_end'), sqlc.arg('status'), sqlc.narg('detail'),
    sqlc.arg('row_count'), sqlc.arg('order_count'), sqlc.arg('to_addresses'),
    sqlc.arg('cc_addresses'), sqlc.arg('bcc_addresses'), sqlc.narg('file_id'),
    sqlc.arg('source')
)
RETURNING *;

-- name: ListAccountsReportRuns :many
-- One brand's registers, newest first. Joined to the file so the page can
-- offer the spreadsheet without a second round trip per row.
SELECT r.*, f.filename, f.size_bytes
FROM accounts_report_runs r
LEFT JOIN file_assets f ON f.id = r.file_id
WHERE r.brand_slug = sqlc.arg('brand_slug')
ORDER BY r.window_end DESC, r.sent_at DESC
LIMIT sqlc.arg('lim');

-- name: GetAccountsReportRun :one
-- One run, for serving its spreadsheet. Scoped by brand as well as id so a
-- token for one brand cannot fetch another's register.
SELECT r.*, f.filename, f.storage_key, f.content_type
FROM accounts_report_runs r
JOIN file_assets f ON f.id = r.file_id
WHERE r.id = sqlc.arg('id') AND r.brand_slug = sqlc.arg('brand_slug');
