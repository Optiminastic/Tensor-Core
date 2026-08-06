-- +goose Up
-- What personalization a product (design) offers: which fields, the allowed
-- fonts/colours/variants, name length limits, and an optional placement zone.
-- NULL means the product is not personalizable. Stored as jsonb so the rule set
-- can grow without a migration per field; the shape is validated in Go.
ALTER TABLE designs ADD COLUMN IF NOT EXISTS personalisation_rules jsonb;

-- +goose Down
ALTER TABLE designs DROP COLUMN IF EXISTS personalisation_rules;
