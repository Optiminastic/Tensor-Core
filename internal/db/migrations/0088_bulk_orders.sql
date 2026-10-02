-- +goose Up
-- Bulk orders and the quotations they produce.
--
-- A business asks for "DNP x 100, SC x 28"; the shop answers with a quotation.
-- This is that conversation: the header is who asked and when, the lines are
-- what they asked for, and the totals are what it comes to.
--
-- PRICES ARE SNAPSHOTTED onto the line, not looked up when the quotation is
-- read. A quotation is a number somebody was given on a date, and Shopify
-- prices move - without the snapshot, reopening last month's quotation would
-- silently show a different total than the customer was quoted, with nothing on
-- screen to say it had changed. Editing the order re-snapshots, which is what
-- makes "edit it and the quotation updates" true and deliberate rather than
-- true by accident.
--
-- The money is numeric, never float: these are rupee amounts that get summed
-- and shown to a customer.
CREATE TABLE IF NOT EXISTS bulk_orders (
    id              uuid PRIMARY KEY,
    -- Human-facing, unique, and what the quotation is titled by.
    quotation_number varchar(32) NOT NULL UNIQUE,
    brand_slug      text NOT NULL REFERENCES brands (slug) ON DELETE RESTRICT,
    -- Who is ordering. One free-text field because a bulk buyer is sometimes a
    -- company and sometimes a person, and forcing that distinction at the form
    -- buys nothing here.
    customer_name   varchar(200) NOT NULL,
    customer_email  varchar(255),
    customer_phone  varchar(40),
    notes           text,
    -- The date the quotation is FOR, which is not its created_at: a quotation
    -- may be written up today and dated to when the order was agreed.
    order_date      date NOT NULL,
    valid_until     date,
    status          varchar(16) NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'sent', 'accepted', 'cancelled')),
    -- Percent off the subtotal, 0-100. Stored as the percentage the operator
    -- typed rather than the rupee amount, because that is the thing they
    -- negotiated and the amount is derivable; storing both invites them to
    -- disagree.
    discount_percent numeric(5, 2) NOT NULL DEFAULT 0
                     CHECK (discount_percent >= 0 AND discount_percent <= 100),
    -- Derived from the lines and the discount, written at save time. Stored
    -- rather than computed on read for the same reason the unit prices are: the
    -- quotation has to say today what it said when it was issued.
    subtotal        numeric(12, 2) NOT NULL DEFAULT 0,
    discount_amount numeric(12, 2) NOT NULL DEFAULT 0,
    total           numeric(12, 2) NOT NULL DEFAULT 0,
    currency        varchar(8) NOT NULL DEFAULT 'INR',
    created_by      varchar(64),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_bulk_orders_brand ON bulk_orders (brand_slug, created_at DESC);

-- One product on a bulk order.
--
-- variant_id is the registry SKU this line is for, and it is nullable with ON
-- DELETE SET NULL on purpose: a quotation outlives the catalogue. If a SKU is
-- retired next year, the line still has to render - which is why sku and
-- product_name are snapshotted beside the reference rather than joined on read.
CREATE TABLE IF NOT EXISTS bulk_order_lines (
    id             uuid PRIMARY KEY,
    bulk_order_id  uuid NOT NULL REFERENCES bulk_orders (id) ON DELETE CASCADE,
    variant_id     uuid REFERENCES product_variants (id) ON DELETE SET NULL,
    sku            varchar(128) NOT NULL,
    product_name   varchar(300) NOT NULL,
    quantity       integer NOT NULL CHECK (quantity > 0),
    unit_price     numeric(12, 2) NOT NULL CHECK (unit_price >= 0),
    line_total     numeric(12, 2) NOT NULL CHECK (line_total >= 0),
    -- Keeps the operator's row order, so the quotation reads in the order it
    -- was entered rather than in whatever order the rows come back.
    position       integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_bulk_order_lines_order
    ON bulk_order_lines (bulk_order_id, position);

-- +goose Down
DROP TABLE IF EXISTS bulk_order_lines;
DROP TABLE IF EXISTS bulk_orders;
