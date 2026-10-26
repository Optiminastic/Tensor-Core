-- +goose Up
-- Whether a SKU's customer is asked to confirm their personalisation.
--
-- Every personalised product is asked today, decided by what the ORDER
-- carries: a line with a name on it gets a link. That is right for the plank
-- range and wrong as a universal rule - a product whose design cannot change
-- after it is bought, or one whose preview is not ready, should ship without
-- interrupting the customer, and there has been no way to say so short of
-- turning the whole feature off for every brand.
--
-- TRUE by default, so nothing changes for anything already selling. Turning
-- it off skips the link AND the thirty-minute hold, so the job renders and
-- batches immediately - which is what a product nobody needs to ask about
-- should do.
ALTER TABLE product_variants
    ADD COLUMN IF NOT EXISTS personalisation_link_enabled boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE product_variants DROP COLUMN IF EXISTS personalisation_link_enabled;
