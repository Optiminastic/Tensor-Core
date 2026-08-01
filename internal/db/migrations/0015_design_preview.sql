-- +goose Up
-- Designers now upload a preview image alongside the model, shown as the design's
-- cover in the grid/table. preview_key is the object-storage key of that image
-- (empty for designs uploaded before this change). Written idempotently.

ALTER TABLE designs ADD COLUMN IF NOT EXISTS preview_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE designs DROP COLUMN IF EXISTS preview_key;
