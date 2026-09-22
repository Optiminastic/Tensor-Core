-- +goose Up
-- Which physical printer a bed was sent to.
--
-- batches.machine_id is a machine_profiles id - a slicing configuration, shared
-- by every unit of a model. On this fleet that is 4 profiles across 14
-- printers: five A2L units answer to one row, five P2S to another. So Tensor
-- has never been able to say which printer ran a bed, only which KIND of
-- printer, and the operator who chose an exact machine in the queue dialog had
-- that choice recorded nowhere.
--
-- It is also what stops the automatic picker piling work onto one unit. Beds
-- are sent one press at a time and take minutes to slice, so five beds queued
-- in two minutes are all still invisible to BambuBuddy's queue when the sixth
-- is ranked. Counting the ones already in flight to each printer is how a
-- machine stops looking idle the instant after it was chosen.
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS fleet_machine_id uuid REFERENCES machines (id) ON DELETE SET NULL;

-- Partial: the only question asked of this column at ranking time is "what is
-- already heading for this printer", and a bed that was never sent to one has
-- no bearing on it.
CREATE INDEX IF NOT EXISTS ix_batches_fleet_machine
    ON batches (fleet_machine_id)
    WHERE fleet_machine_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_batches_fleet_machine;
ALTER TABLE batches DROP COLUMN IF EXISTS fleet_machine_id;
