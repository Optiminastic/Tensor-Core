-- Removes four columns on batches that no migration in this repo ever created.
--
-- gcode_key, sliced_at, slice_status and slice_error predate the Go rewrite.
-- 0036_batch_slice added plate_sliced_at and plate_slice_error to replace them
-- and never dropped the originals, so the live table carried 39 columns while
-- schema.sql - which is what sqlc generates from - described 35.
--
-- That is not cosmetic. Eight batch queries are `SELECT b.*`, and sqlc scans a
-- fixed list of destinations, so every one of them failed the moment it ran:
-- pgx refuses when the row has more fields than the struct. Production answered
-- GET /batches with 500 in under three milliseconds and the Batch Management
-- page showed nothing at all.
--
-- Safe to drop, checked rather than assumed: all four were NULL or empty in
-- every row of the table, no Go code names them, and batches.sql never selects
-- or writes them. The slice state that IS live lives in plate_sliced_at and
-- plate_slice_error.
--
-- IF EXISTS on each, because a database created purely from these migrations
-- never had them - only ones carried over from the old schema do.

-- +goose Up
ALTER TABLE batches
    DROP COLUMN IF EXISTS gcode_key,
    DROP COLUMN IF EXISTS sliced_at,
    DROP COLUMN IF EXISTS slice_status,
    DROP COLUMN IF EXISTS slice_error;

-- +goose Down
-- Restored as they were: nullable, no defaults, carrying nothing. The down
-- migration exists to make this reversible, not to bring back data - there was
-- none to lose.
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS gcode_key    text,
    ADD COLUMN IF NOT EXISTS sliced_at    timestamptz,
    ADD COLUMN IF NOT EXISTS slice_status varchar,
    ADD COLUMN IF NOT EXISTS slice_error  text;
