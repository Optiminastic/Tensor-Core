-- +goose Up
-- Snapshot the matched design's bounding box and slice-derived
-- support/purge weight and colour-change count onto the job row at creation
-- time, extending the existing print-time/filament-grams snapshot pattern
-- (see applyMatch/design_match.go) - so these numbers don't silently change
-- if the design is re-sliced after the job was created, and so batch-build
-- time (bedpack.UnitFootprint, filament planning) never needs to re-join
-- file_assets/slice_metrics per job. All nullable: a job with no matched
-- design (issue_reason set) or no measured file/slice yet has none of them.
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS bbox_x_mm numeric(10, 2);
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS bbox_y_mm numeric(10, 2);
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS bbox_z_mm numeric(10, 2);
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS support_weight_g numeric(10, 3);
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS purge_weight_g numeric(10, 3);
ALTER TABLE production_jobs ADD COLUMN IF NOT EXISTS colour_count integer;

-- +goose Down
ALTER TABLE production_jobs DROP COLUMN IF EXISTS colour_count;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS purge_weight_g;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS support_weight_g;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS bbox_z_mm;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS bbox_y_mm;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS bbox_x_mm;
