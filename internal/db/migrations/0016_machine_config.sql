-- +goose Up
-- Machines gain slicing configuration so a design can be sliced on a specific
-- machine: the Bambu profile family + nozzle, the flow type, its supported
-- filaments (with a default) and default colour, and its allowed layer-height
-- range. Cost (machine_hour_cost) is untouched - a separate admin surface owns
-- it, since Operators may edit machine config but must never see cost. Idempotent.

ALTER TABLE machine_profiles
    ADD COLUMN IF NOT EXISTS family              varchar(16) NOT NULL DEFAULT 'H2S',
    ADD COLUMN IF NOT EXISTS nozzle_mm           numeric(3, 2) NOT NULL DEFAULT 0.4,
    ADD COLUMN IF NOT EXISTS flow                varchar(16) NOT NULL DEFAULT 'standard',
    ADD COLUMN IF NOT EXISTS default_colour      varchar(40),
    ADD COLUMN IF NOT EXISTS layer_height_min_mm numeric(4, 2) NOT NULL DEFAULT 0.08,
    ADD COLUMN IF NOT EXISTS layer_height_max_mm numeric(4, 2) NOT NULL DEFAULT 0.28,
    ADD COLUMN IF NOT EXISTS supported_filaments jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS is_default          boolean NOT NULL DEFAULT false;

-- The machine a design slices on. NULL means "not chosen" - the worker falls back
-- to the built-in H2S 0.4 profile, so existing designs keep working. ON DELETE SET
-- NULL so removing a machine never deletes designs.
ALTER TABLE designs
    ADD COLUMN IF NOT EXISTS machine_id uuid REFERENCES machine_profiles (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE designs DROP COLUMN IF EXISTS machine_id;
ALTER TABLE machine_profiles
    DROP COLUMN IF EXISTS family,
    DROP COLUMN IF EXISTS nozzle_mm,
    DROP COLUMN IF EXISTS flow,
    DROP COLUMN IF EXISTS default_colour,
    DROP COLUMN IF EXISTS layer_height_min_mm,
    DROP COLUMN IF EXISTS layer_height_max_mm,
    DROP COLUMN IF EXISTS supported_filaments,
    DROP COLUMN IF EXISTS is_default;
