-- +goose Up
-- Dual-nozzle machines (BBL H2C) can run a different flow setting per nozzle.
-- `flow` is the left nozzle's flow; this is the right nozzle's - null for a
-- single-nozzle profile, matching right_nozzle_mm's existing nullability.
ALTER TABLE machine_profiles ADD COLUMN IF NOT EXISTS right_flow varchar(16);

-- +goose Down
ALTER TABLE machine_profiles DROP COLUMN IF EXISTS right_flow;
