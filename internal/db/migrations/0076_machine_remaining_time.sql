-- +goose Up
-- What the printer says is left on the plate it is running right now.
--
-- The scheduler has been projecting machine availability from nothing. Its
-- MachineFreeAt reads machines.print_started_at and batch_total_time_minutes,
-- and those two are written only by cmd/simulator and a manual PATCH - the
-- fleet sync deliberately excludes them, because they record what Tensor ASKED
-- a unit to run, not what it is doing. So on this fleet all 14 printers carry
-- NULL in both, MachineFreeAt returns "now" for every one of them, and ranking
-- machines by when they come free has been ranking them by nothing at all.
--
-- Meanwhile the 60-second sync has been fetching exactly this number from
-- BambuBuddy on every pass and throwing it away.
--
-- Separate columns rather than filling in that existing pair, because the pair
-- means something else and something a reader depends on: the fleet page
-- renders batch_total_time_minutes as the plate's TOTAL. Writing remaining time
-- there would show a total that counts down.
--
-- NAME COLLISION, and it has already caught one reader: batches.print_started_at
-- is a DIFFERENT COLUMN IN A DIFFERENT TABLE, written by print_completion.go
-- when a print finishes. Nothing here touches it.
ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS remaining_minutes integer,
    -- When the printer said it. A remaining time with no observation time is
    -- unusable: the sync runs every 60s but a machine that has gone unreachable
    -- keeps its last row, and "40 minutes left" from an hour ago is not a
    -- smaller number, it is a lie. Readers fence on this.
    ADD COLUMN IF NOT EXISTS remaining_observed_at timestamptz;

-- +goose Down
ALTER TABLE machines
    DROP COLUMN IF EXISTS remaining_observed_at,
    DROP COLUMN IF EXISTS remaining_minutes;
