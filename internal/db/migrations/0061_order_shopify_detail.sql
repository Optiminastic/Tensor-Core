-- +goose Up
-- Everything Shopify's own order page shows, so Tensor can show the same thing.
--
-- Until now an order kept its number, financial status and total, and threw the
-- rest away. An operator checking what a customer actually bought - which
-- variant, at what price, what the discount was, whether it was paid - had to
-- open Shopify, which defeats the point of importing the order at all.
--
-- Money is nullable rather than DEFAULT 0. An order imported before this
-- migration genuinely does not know what its subtotal was, and a zero would
-- render as "Subtotal 0.00" - a confident wrong number is worse than a dash.
-- Re-syncing backfills them.
ALTER TABLE orders
    -- When Shopify says the order was placed, as against imported_at (when
    -- Tensor first saw it). They can be weeks apart on a backfill, and it is
    -- the customer's date that belongs on the page.
    ADD COLUMN IF NOT EXISTS placed_at          timestamptz,
    ADD COLUMN IF NOT EXISTS note               text,
    -- Shopify's order-level custom attributes - the "Additional details" panel
    -- (e.g. "__ref_id"). Same verbatim treatment as line-item properties.
    ADD COLUMN IF NOT EXISTS attributes         jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS tags               jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS fulfillment_status varchar(32),
    -- "Online Store", "POS" - the "from Online Store" on Shopify's header.
    ADD COLUMN IF NOT EXISTS source_name        varchar(64),
    ADD COLUMN IF NOT EXISTS subtotal_price     numeric(10, 2),
    ADD COLUMN IF NOT EXISTS total_discounts    numeric(10, 2),
    ADD COLUMN IF NOT EXISTS total_shipping     numeric(10, 2),
    -- What the customer has actually paid. Distinct from total_price: a COD or
    -- partially-refunded order differs, and "Paid" is its own line on Shopify's
    -- summary for that reason.
    ADD COLUMN IF NOT EXISTS total_received     numeric(10, 2),
    -- The human labels beside those amounts, e.g. "20% off for orders above
    -- ₹1000" and "FREE DISPATCH - BEST DEAL". Stored rather than re-derived:
    -- they are the store's own wording and Tensor has no way to reconstruct it.
    ADD COLUMN IF NOT EXISTS discount_title     text,
    ADD COLUMN IF NOT EXISTS shipping_title     text,
    -- Protected customer data. These stay null until the app is granted
    -- protected-data access; the columns exist so that approval is a re-sync
    -- rather than another migration.
    ADD COLUMN IF NOT EXISTS shipping_address   jsonb,
    ADD COLUMN IF NOT EXISTS billing_address    jsonb;

-- +goose Down
ALTER TABLE orders
    DROP COLUMN IF EXISTS placed_at,
    DROP COLUMN IF EXISTS note,
    DROP COLUMN IF EXISTS attributes,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS fulfillment_status,
    DROP COLUMN IF EXISTS source_name,
    DROP COLUMN IF EXISTS subtotal_price,
    DROP COLUMN IF EXISTS total_discounts,
    DROP COLUMN IF EXISTS total_shipping,
    DROP COLUMN IF EXISTS total_received,
    DROP COLUMN IF EXISTS discount_title,
    DROP COLUMN IF EXISTS shipping_title,
    DROP COLUMN IF EXISTS shipping_address,
    DROP COLUMN IF EXISTS billing_address;
