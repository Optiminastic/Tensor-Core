-- +goose Up
-- The code a bulk order's production jobs are numbered from.
--
-- A Shopify job is numbered from its order - JOB-115464 - which traces a plank
-- on the floor back to the customer who bought it. A bulk order has no Shopify
-- order number, and the shop wants its jobs told apart from storefront work at
-- a glance: an order for Optiminastic Media Pvt Ltd is given the code OMPT, and
-- its jobs become OMPT-1, OMPT-2 and so on in order.
--
-- UNIQUE, and that is the whole point of storing it rather than deriving it.
-- job_number is unique across every job in the system, so two bulk orders
-- sharing a code would collide on OMPT-1 and the second order would fail to
-- approve halfway through. Enforcing it here means the clash is reported when
-- the code is typed, not when the hundredth job is inserted.
--
-- Nullable because it is set at APPROVAL, not at creation: a quotation may be
-- written, edited and rejected without ever becoming production work.
ALTER TABLE bulk_orders
    ADD COLUMN IF NOT EXISTS job_code varchar(16);

CREATE UNIQUE INDEX IF NOT EXISTS uq_bulk_orders_job_code
    ON bulk_orders (upper(job_code)) WHERE job_code IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_bulk_orders_job_code;
ALTER TABLE bulk_orders DROP COLUMN IF EXISTS job_code;
