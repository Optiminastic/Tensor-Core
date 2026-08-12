-- +goose Up
-- One physical unit of a multi-part product, and the individual part slots that
-- must all be built before it can be assembled and shipped.
--
-- assembly_groups: one row per ordered unit of a multi-part product. An order line
-- of quantity N for a multi-part SKU yields N groups. Single-part products create
-- no group at all (the legacy one-job-per-line path is untouched).
CREATE TABLE IF NOT EXISTS assembly_groups (
    id         uuid PRIMARY KEY,
    order_id   uuid REFERENCES orders (id) ON DELETE SET NULL,
    design_sku varchar(128),
    unit_index integer NOT NULL DEFAULT 1,
    status     varchar(32) NOT NULL DEFAULT 'printing',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_assembly_groups_order ON assembly_groups (order_id);

-- assembly_group_parts: one row per part slot in a unit, keyed by
-- (assembly_group_id, part_role, part_instance). part_uid is the stable,
-- system-minted, never-reused id for the slot (PART-XXXXXX); it is carried across
-- reprints. job_id points at the slot's CURRENT print attempt - a reprint repoints
-- it to the new job, so the slot (and its part_uid) survives every reprint.
CREATE TABLE IF NOT EXISTS assembly_group_parts (
    id                uuid PRIMARY KEY,
    assembly_group_id uuid NOT NULL REFERENCES assembly_groups (id) ON DELETE CASCADE,
    job_id            uuid REFERENCES production_jobs (id) ON DELETE SET NULL,
    part_role         varchar(64) NOT NULL,
    part_instance     integer NOT NULL DEFAULT 1,
    part_uid          varchar(64) NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS assembly_group_parts_uid_key ON assembly_group_parts (part_uid);
CREATE UNIQUE INDEX IF NOT EXISTS assembly_group_parts_slot_key
    ON assembly_group_parts (assembly_group_id, part_role, part_instance);
CREATE INDEX IF NOT EXISTS ix_assembly_group_parts_group ON assembly_group_parts (assembly_group_id);
CREATE INDEX IF NOT EXISTS ix_assembly_group_parts_job ON assembly_group_parts (job_id);

-- +goose Down
DROP TABLE IF EXISTS assembly_group_parts;
DROP TABLE IF EXISTS assembly_groups;
