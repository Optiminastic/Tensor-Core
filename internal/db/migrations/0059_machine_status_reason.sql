-- +goose Up
-- Numbered 0059, not 0045, for the reason spelled out in 0058: versions up
-- to 57 are already claimed by a fork's lineage in the shared database, and
-- goose skips a lower number without reporting anything.
-- Why a machine is in the status it is in, in BambuBuddy's own words.
--
-- Only ever meaningful for the 'error' status: an idle or running printer has
-- no reason to give. Stored rather than derived on read because the fleet table
-- is what every list page reads, and re-querying BambuBuddy per row to explain
-- a status the sync already knew would be the same call fifteen times over.
--
ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS status_reason text;

COMMENT ON COLUMN machines.status_reason IS
    'HMS text from the printer explaining an error status; null otherwise.';

-- +goose Down
ALTER TABLE machines DROP COLUMN IF EXISTS status_reason;
