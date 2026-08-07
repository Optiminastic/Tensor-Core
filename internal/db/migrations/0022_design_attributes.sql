-- Free-form design attributes captured on upload (spec Step 1): product type,
-- personalisation type, colour count, add-ons, packaging type. Stored as jsonb so
-- the set can grow without a migration per field; the shape is validated in Go.

-- +goose Up
ALTER TABLE designs ADD COLUMN IF NOT EXISTS attributes jsonb;

-- +goose Down
ALTER TABLE designs DROP COLUMN IF EXISTS attributes;
