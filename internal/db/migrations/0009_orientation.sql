-- +goose Up
-- The optimal-orientation recommendation (least support material) computed from
-- the model geometry. JSON so the shape can evolve without a migration; null when
-- the format is unsupported (STEP) or the mesh could not be read. Advisory only:
-- it never changes the costed slice. Idempotent.
ALTER TABLE slice_metrics ADD COLUMN IF NOT EXISTS orientation jsonb;

-- +goose Down
ALTER TABLE slice_metrics DROP COLUMN IF EXISTS orientation;
