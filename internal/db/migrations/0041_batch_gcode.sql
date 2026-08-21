-- A batch's printable artefact. Until now a batch stopped at merged_file_id: the
-- merged plate STL, which a printer cannot consume. gcode_key points at the
-- sliced plate (gcode/batches/<batchID>.gcode.3mf in object storage), which is
-- what actually gets sent to a printer.
--
-- Deliberately a bare object key, not a file_assets row: it mirrors how a
-- design's G-code is recorded (slice_metrics.gcode_key), and file_assets models
-- uploaded source models rather than derived slicer output.
--
-- slice_status/slice_error make a plate slice observable without another table -
-- the plate slice is asynchronous, so "no gcode yet" has to be tellable from
-- "the slice failed".

-- +goose Up
ALTER TABLE batches ADD COLUMN IF NOT EXISTS gcode_key text;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS sliced_at timestamptz;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS slice_status varchar(32);
ALTER TABLE batches ADD COLUMN IF NOT EXISTS slice_error text;

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS slice_error;
ALTER TABLE batches DROP COLUMN IF EXISTS slice_status;
ALTER TABLE batches DROP COLUMN IF EXISTS sliced_at;
ALTER TABLE batches DROP COLUMN IF EXISTS gcode_key;
