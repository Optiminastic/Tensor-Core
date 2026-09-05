-- +goose Up
-- The last two columns of Shopify's own order list.
--
-- 0061 captured most of what that list shows; these are the two it did not.
-- Both are display-only today, but they are the columns an operator scans when
-- chasing "where is it" and "did it come back", which is exactly the pair that
-- currently forces them out of Tensor and into Shopify.
--
-- delivery_status is the carrier's view (IN_TRANSIT, DELIVERED, ...), taken
-- from the order's first fulfilment. Null until something ships - an
-- unfulfilled order has no fulfilments at all, which is why it must be
-- nullable rather than defaulted to a string meaning "none".
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS delivery_status varchar(32),
    ADD COLUMN IF NOT EXISTS return_status   varchar(32);

COMMENT ON COLUMN orders.delivery_status IS
    'Carrier status of the order''s first fulfilment, e.g. IN_TRANSIT / DELIVERED.';
COMMENT ON COLUMN orders.return_status IS
    'Shopify''s OrderReturnStatus, e.g. NO_RETURN / RETURNED / IN_PROGRESS.';

-- +goose Down
ALTER TABLE orders
    DROP COLUMN IF EXISTS delivery_status,
    DROP COLUMN IF EXISTS return_status;
