-- +goose Up
-- Bridges a physical fleet machine (machines) to its slicing config
-- (machine_profiles). batches.machine_id already points at machine_profiles
-- (which config to run); the scheduler resolves that to a specific physical
-- unit by matching machines.machine_profile_id, rather than adding a second,
-- competing machine_id column to batches.
ALTER TABLE machines ADD COLUMN IF NOT EXISTS machine_profile_id uuid
    REFERENCES machine_profiles (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_machines_machine_profile_id ON machines (machine_profile_id);

-- +goose Down
DROP INDEX IF EXISTS idx_machines_machine_profile_id;
ALTER TABLE machines DROP COLUMN IF EXISTS machine_profile_id;
