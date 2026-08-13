-- Records the outcome of slicing a batch's merged plate.
--
-- Until now batches.total_print_time_minutes was MAX(each job's own per-plate
-- estimate) - see batchTimeFromJobs. That number describes a bed holding one
-- design's units, not the mixed bed that will actually print, and it is what
-- feeds MachineFreeAt and therefore every machine assignment. The merged plate
-- was already being built and stored (buildMergedPlate -> batches/<id>/*.stl);
-- it was simply never sliced.
--
-- These columns make a plate slice observable. Without them a failed slice is
-- indistinguishable from one that has not run yet: the batch just keeps
-- carrying the MAX approximation with nothing saying why.

-- +goose Up
ALTER TABLE batches
    -- When the merged plate was last sliced. NULL means the estimate still
    -- comes from batchTimeFromJobs, not from a real slice of this bed.
    ADD COLUMN IF NOT EXISTS plate_sliced_at    timestamptz,
    -- The slicer's own failure reason, cleared on the next success. Set only
    -- after River exhausts its retries, so a transient failure mid-retry does
    -- not flap this column.
    ADD COLUMN IF NOT EXISTS plate_slice_error  text;

-- Finding the batches still carrying an approximation is the one query an
-- operator or a backfill actually runs; partial so the index stays small once
-- most batches have been sliced.
CREATE INDEX IF NOT EXISTS ix_batches_unsliced
    ON batches (created_at DESC)
    WHERE plate_sliced_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_batches_unsliced;
ALTER TABLE batches
    DROP COLUMN IF EXISTS plate_slice_error,
    DROP COLUMN IF EXISTS plate_sliced_at;
