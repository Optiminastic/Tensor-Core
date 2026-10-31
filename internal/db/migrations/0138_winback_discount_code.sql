-- +goose Up
-- The code we minted for one customer, kept so the conversion can be proved.
--
-- Every win-back message mints its OWN discount - "Win-back #67129291899093",
-- valid for a window, sent to exactly one phone - and then threw the code
-- away. Which meant the one question the shop actually wants answered was
-- unanswerable: of the carts that came back, which came back BECAUSE we
-- called and messaged?
--
-- Keeping it makes that provable rather than estimated. Shopify reports a
-- checkout's discountCodes as they stand, and a win-back code reaches a
-- checkout only by the customer following our link - measured live, two
-- checkouts carried SATYAVATHI10 and KIRAN10 while still abandoned, which is
-- the restored cart with the code applied and the purchase not yet made. So
-- the code alone proves the click, and the code together with completed_at
-- proves the recovery was ours.
--
-- NULL for everything sent before this existed, and that is shown as "we
-- cannot tell" rather than as a failure to convert.
ALTER TABLE abandoned_checkout_calls
    ADD COLUMN IF NOT EXISTS discount_code varchar(64);

-- +goose Down
ALTER TABLE abandoned_checkout_calls DROP COLUMN IF EXISTS discount_code;
