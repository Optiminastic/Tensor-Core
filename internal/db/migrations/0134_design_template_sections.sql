-- +goose Up
-- Which heading a design file is filed under on the Designs tab.
--
-- A LABEL, AND NOTHING ELSE READS IT. The renderer resolves a file through
-- variant_designs - which product prints from which key, in which role - and
-- that is the only thing that decides what a customer receives. This decides
-- which accordion the row appears in, so somebody looking at a shelf of .scad
-- files can tell at a glance which is which. Setting it changes no model, no
-- job and no order.
--
-- That separation is the point. The first attempt at this let the Designs tab
-- assign a file to a product, which meant "tidy the list" and "change what
-- prints" were the same gesture - and the second one is not something to do
-- by accident from a file browser.
--
-- KEYED ON THE TEMPLATE KEY, not on a design_templates row, for two reasons:
--
--   - design_templates is versioned. Uploading a new file supersedes the
--     active row and inserts another, so a label stored on the row would be
--     lost by the next upload - which is exactly when somebody is looking at
--     the list.
--   - the templates this binary ships with have no row at all. A label that
--     could only live on an uploaded row could not describe half the list.
--
-- Lower-cased, matching uq_design_template_active and every other place a key
-- is compared, so "PWDH" and "pwdh" cannot be filed under two headings.
CREATE TABLE IF NOT EXISTS design_template_sections (
    template_key varchar(64) PRIMARY KEY,
    -- 'print', 'preview' or 'other'. Deliberately NOT a foreign key to a list
    -- of sections: the headings are a property of one screen, and a table of
    -- them would be ceremony around three words.
    section      varchar(32) NOT NULL,
    updated_by   varchar(64) NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS design_template_sections;
