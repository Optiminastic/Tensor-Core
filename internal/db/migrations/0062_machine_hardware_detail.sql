-- +goose Up
-- What each printer actually is, not just what it is doing.
--
-- The fleet table stored identity and liveness only, so Tensor knew a printer
-- was called "H1" and was idle but not that it was an H2C at a particular
-- address. With thirteen printers across three models (A2L, H2C, P2S) that is
-- the difference between a list an operator can act on and a list of names.
--
-- It also matters to scheduling: a plate sliced for one model cannot run on
-- another, and the model is how that will be checked.
ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS model        varchar(64),
    ADD COLUMN IF NOT EXISTS location     varchar(255),
    ADD COLUMN IF NOT EXISTS ip_address   varchar(64),
    ADD COLUMN IF NOT EXISTS nozzle_count integer;

COMMENT ON COLUMN machines.model IS
    'Printer model as BambuBuddy reports it, e.g. A2L / H2C / P2S.';

-- +goose Down
ALTER TABLE machines
    DROP COLUMN IF EXISTS model,
    DROP COLUMN IF EXISTS location,
    DROP COLUMN IF EXISTS ip_address,
    DROP COLUMN IF EXISTS nozzle_count;
