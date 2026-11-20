-- +goose Up
-- Tensor's own copy of the settings a plate is sliced with.
--
-- Until now the slicer configuration lived only in BambuBuddy: a SKU named a
-- pipeline (0085) and BambuBuddy's presets were the settings. Tensor could not
-- edit them (API keys cannot change presets) and could not slice with them,
-- because it never held them.
--
-- A profile is one pipeline's printer, process and filament presets, stored
-- whole as BambuBuddy's `setting` objects, plus Tensor's own overrides on top
-- of the process. The slice worker turns a profile into the preset files the
-- Bambu Studio CLI loads, so a Tensor slice uses exactly the settings a
-- BambuBuddy slice of the same pipeline would.
--
-- Mirrors a pipeline by id, so the SKU mapping in sku_slicer_pipelines keeps
-- working unchanged: SKU -> pipeline -> profile. A profile with no pipeline is
-- allowed for settings that exist only in Tensor.
CREATE TABLE IF NOT EXISTS slicing_profiles (
    id                   uuid PRIMARY KEY,
    bambu_pipeline_id    integer,
    name                 varchar(200) NOT NULL,
    -- H2C / A2L / P2S: a sliced file only runs on the model it was sliced for.
    machine_family       varchar(16)  NOT NULL,
    printer_preset_name  varchar(200) NOT NULL,
    printer_preset       jsonb        NOT NULL,
    process_preset_name  varchar(200) NOT NULL,
    process_preset       jsonb        NOT NULL,
    filament_preset_name varchar(200) NOT NULL,
    filament_preset      jsonb        NOT NULL,
    -- Process keys Tensor changes on top of the imported process preset,
    -- {"sparse_infill_density": "15%"}. Kept apart from the preset so a
    -- re-import from BambuBuddy refreshes the base and keeps these.
    overrides            jsonb        NOT NULL DEFAULT '{}'::jsonb,
    -- Bumped on every change to the presets or the overrides. A batch records
    -- the version it was sliced with, so "what settings printed this bed" has
    -- an answer after the profile moves on.
    version              integer      NOT NULL DEFAULT 1,
    imported_at          timestamptz,
    updated_by           varchar(64)  NOT NULL DEFAULT '',
    created_at           timestamptz  NOT NULL DEFAULT now(),
    updated_at           timestamptz  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_slicing_profiles_pipeline
    ON slicing_profiles (bambu_pipeline_id) WHERE bambu_pipeline_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_slicing_profiles_pipeline;
DROP TABLE IF EXISTS slicing_profiles;
