-- +goose Up
-- Phase 2 of the production pipeline: batches (jobs grouped onto one machine bed)
-- and filament inventory. A batch snapshots the bed-packing result - units, print
-- time, filament, utilisation - and points at the merged plate STL in storage.
-- production_jobs.batch_id, added FK-less in 0010, gets its foreign key here.

-- A batch of production jobs printed together on one machine bed. status:
-- pending_approval (auto-created) -> open (approved, merged, machine assigned)
-- -> in_progress -> completed.
CREATE TABLE IF NOT EXISTS batches (
    id                              uuid PRIMARY KEY,
    batch_number                    varchar(64) NOT NULL,
    machine_id                      uuid REFERENCES machine_profiles (id) ON DELETE SET NULL,
    status                          varchar(32) NOT NULL DEFAULT 'open',
    approved_by                     varchar(64),
    approved_at                     timestamptz,
    material_shortage               boolean NOT NULL DEFAULT false,
    merged_file_id                  uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    preview_file_id                 uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    units_per_bed                   integer,
    total_print_time_minutes        integer,
    effective_time_per_unit_minutes numeric(10, 2),
    total_filament_grams            numeric(10, 2),
    bed_utilization_percent         numeric(5, 2),
    packing_strategy                varchar(32),
    created_at                      timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_batches_created ON batches (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS ix_batches_machine_status ON batches (machine_id, status);

-- Now that batches exists, wire the job -> batch foreign key. Guard with a
-- catalog check so the idempotent re-run does not fail on an existing constraint.
-- The dollar-quoted block is one statement, so it is fenced for the goose runner.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_production_jobs_batch_id'
    ) THEN
        ALTER TABLE production_jobs
            ADD CONSTRAINT fk_production_jobs_batch_id
            FOREIGN KEY (batch_id) REFERENCES batches (id) ON DELETE SET NULL;
    END IF;
END $$;
-- +goose StatementEnd

-- Physical filament stock, keyed by material (+ optional colour). Distinct from
-- material_profiles, which is cost configuration. grams_available may go negative
-- (a print that wasted more than recorded) - it is a running tally, not a clamp.
CREATE TABLE IF NOT EXISTS filament_inventory (
    id                  uuid PRIMARY KEY,
    material            varchar(255) NOT NULL,
    colour              varchar(255),
    grams_available     numeric(10, 2) NOT NULL DEFAULT 0,
    reorder_level_grams numeric(10, 2) NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
-- One row per (material, colour). NULL colours are treated as a single bucket via
-- COALESCE so upserts of an untinted material do not create duplicates.
CREATE UNIQUE INDEX IF NOT EXISTS uq_filament_material_colour
    ON filament_inventory (material, COALESCE(colour, ''));

-- +goose Down
DROP TABLE IF EXISTS filament_inventory;
ALTER TABLE production_jobs DROP CONSTRAINT IF EXISTS fk_production_jobs_batch_id;
DROP TABLE IF EXISTS batches;
