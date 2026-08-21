-- The rest of what slicing the merged plate already measures.
--
-- SetBatchPlateSliceResult stored only time and total filament, so everything
-- else Bambu Studio reported about the plate - how much of that filament is
-- support, how much is purged at colour changes, how many layers, how many
-- colour changes - was computed, used to derive those two numbers, and then
-- discarded. Batch Management could show none of it.
--
-- These are the plate's own figures, not a sum of per-job estimates. That
-- distinction is the whole reason plate slicing exists: printing five parts
-- together is not five separate prints, and the purge between colours is a
-- property of the combined plate, not of any part on it.

-- +goose Up
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS total_layers          integer,
    ADD COLUMN IF NOT EXISTS support_grams         numeric(10, 2),
    ADD COLUMN IF NOT EXISTS purge_grams           numeric(10, 2),
    ADD COLUMN IF NOT EXISTS colour_changes        integer,
    -- Filament split by colour, as [{"colour":"Black","material":"PLA
    -- Basics","grams":123.4}]. JSONB rather than a child table: it is written
    -- once by the slice worker, read whole by the UI, and never queried across
    -- batches.
    ADD COLUMN IF NOT EXISTS filament_by_colour    jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE batches
    DROP COLUMN IF EXISTS filament_by_colour,
    DROP COLUMN IF EXISTS colour_changes,
    DROP COLUMN IF EXISTS purge_grams,
    DROP COLUMN IF EXISTS support_grams,
    DROP COLUMN IF EXISTS total_layers;
