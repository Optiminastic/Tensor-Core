-- +goose Up

-- The daily sales register: who it goes to, and what has already been sent.
--
-- ONE ROW PER BRAND, like every other per-brand setting here. A brand with no
-- row has never been configured, which is different from one configured and
-- switched off - and the difference matters, because the worker must not
-- invent recipients for a shop nobody has set this up for.
--
-- ADDRESSES AS text[], NOT A JOIN TABLE. They are three ordered lists of
-- strings edited together on one screen and written as a set; a child table
-- would buy nothing and cost a transaction on every save.
CREATE TABLE IF NOT EXISTS accounts_report_settings (
    brand_slug   varchar(64) PRIMARY KEY REFERENCES brands(slug) ON DELETE CASCADE,

    -- OFF UNTIL SOMEBODY TURNS IT ON. This mails a real accountant, so the
    -- safe default is silence - the same reasoning as the personalisation
    -- check's consent switch.
    enabled      boolean NOT NULL DEFAULT false,

    to_addresses  text[] NOT NULL DEFAULT '{}',
    cc_addresses  text[] NOT NULL DEFAULT '{}',
    bcc_addresses text[] NOT NULL DEFAULT '{}',

    -- The tax assertion, held per brand rather than compiled in.
    --
    -- Shopify reports no tax at all on this store - every line carries an
    -- IGST line at rate 0.18 priced 0.00, and the order total equals its
    -- subtotal - so the tax cannot be read and has to be derived from a rate
    -- somebody asserts. Here, so it can be corrected without a deploy.
    gst_percent  numeric(5, 2) NOT NULL DEFAULT 18.00,
    -- Decides CGST+SGST against IGST: a sale inside the seller's own state
    -- splits, one crossing a border does not.
    seller_state varchar(64) NOT NULL DEFAULT 'Maharashtra',

    -- WHAT WAS LAST SENT, and the reason this table is not just a settings
    -- row. The window is noon-to-noon, so the worker wakes repeatedly inside
    -- one window; without a record of the last window it closed, a restart or
    -- a second process would mail the accountant the same day twice.
    last_window_end timestamptz,
    last_sent_at    timestamptz,
    last_row_count  integer,
    -- Why the last attempt failed, in the server's own words. Null on a
    -- healthy send. On screen rather than only in a log, because a report
    -- that silently stops arriving is the failure this whole feature has.
    last_error      text,

    updated_by   text,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS accounts_report_settings;
