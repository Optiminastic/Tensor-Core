-- +goose Up
-- What a two-nozzle printer's fixed nozzle holds, and which nozzle that is.
--
-- The H2C has two extruders. One is fed from the AMS and takes whatever colour
-- the shop loads; the other is fed by an external spool and, on this floor,
-- always holds white - it is the plank body on every plate.
--
-- Tensor could not see that filament at all. It reads a printer's AMS units and
-- their trays, and an external spool is not an AMS unit: it reports as
-- ams_id 254 under extruder_slots, which nothing read. So the colour gate
-- refused all three H2Cs with "no spool has been confirmed as #FFFFFF" for a
-- spool that was physically loaded, and those machines have never printed
-- anything Tensor planned.
--
-- It cannot be detected either. An external spool has no RFID, so the printer
-- itself reports its colour as 00000000 - the operator is the only one who
-- knows. Hence a column rather than a sync field.
ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS fixed_nozzle_colour varchar(16);

-- Which extruder the fixed spool feeds, 0-based, as the printer reports it.
--
-- Synced rather than configured: extruder_slots names the one fed by ams_id 254
-- and that is not a matter of opinion. Null on a single-nozzle machine, which
-- is every A2L and P2S here, and what tells the slice path not to send a
-- filament map at all.
ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS fixed_nozzle_index integer;

-- +goose Down
ALTER TABLE machines DROP COLUMN IF EXISTS fixed_nozzle_index;
ALTER TABLE machines DROP COLUMN IF EXISTS fixed_nozzle_colour;
