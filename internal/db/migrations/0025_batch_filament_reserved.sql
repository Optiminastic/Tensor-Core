-- +goose Up
-- Guards against double-reserving a batch's filament: set true the moment
-- approveBatch debits stock (Draft/pending_approval -> Locked/open), checked
-- alongside the status transition itself so a lost race can't double-debit.
ALTER TABLE batches ADD COLUMN IF NOT EXISTS filament_reserved boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS filament_reserved;
