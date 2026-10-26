-- +goose Up
-- The ready-made plate a printed component is replenished from.
--
-- A component nobody personalises is the same every time, so slicing one again
-- for every run is work that answers a question already answered. This stores
-- the finished plate per machine class: printing more red hearts becomes
-- sending a saved file rather than arranging and slicing a bed.
--
-- WHAT IT WAS SLICED WITH IS RECORDED, and that is the whole safety of storing
-- G-code rather than a 3MF. A sliced plate is only valid for the filament and
-- the profile it assumed; change either and the file is quietly wrong, and
-- nothing in it says so. pipeline_name, material and colour let the screen say
-- "sliced for Red PLA on DNP-H2C" instead of an operator finding out at the
-- printer.
--
-- A SNAPSHOT, not a join - the same decision sku_slicer_pipelines.pipeline_name
-- already makes (migration 0085): a pipeline deleted in BambuBuddy has to
-- produce "the one this used is gone" rather than a silent fallback to
-- somebody else's settings, and only a stored name can say which it was.
CREATE TABLE IF NOT EXISTS component_plates (
    id uuid PRIMARY KEY,
    inventory_item_id uuid NOT NULL REFERENCES inventory_items (id) ON DELETE CASCADE,
    -- H2C | A2L | P2S, the vocabulary machineFamilies() already uses.
    machine_family varchar(16) NOT NULL,
    file_id uuid NOT NULL REFERENCES file_assets (id) ON DELETE RESTRICT,
    -- How many come off one plate, so somebody can work out how many plates a
    -- shortfall needs without opening the file.
    units_per_plate integer NOT NULL,
    pipeline_name varchar(200),
    material varchar(20),
    colour varchar(60),
    notes varchar(500),
    uploaded_by varchar(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_component_plate_units CHECK (units_per_plate > 0)
);

-- One plate per component per machine class. Upper-cased like
-- uq_sku_pipeline_family, because the class is typed in two places and they
-- disagree about case more often than anybody expects.
CREATE UNIQUE INDEX IF NOT EXISTS uq_component_plate_family
    ON component_plates (inventory_item_id, upper(machine_family));

-- +goose Down
DROP TABLE IF EXISTS component_plates;
