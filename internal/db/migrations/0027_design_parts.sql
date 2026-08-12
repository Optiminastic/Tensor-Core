-- +goose Up
-- Multi-part products: a design may declare named parts (the "recipe"), e.g. a
-- product that prints as body + lid + two legs. A design with no design_parts row
-- is a single-part product and behaves exactly as before, so this is a pure add.
--
-- role is the human-facing part name (unique per design). quantity is how many of
-- that identical part one product needs (two legs -> quantity 2). print_file_id and
-- the material/colour/nozzle columns are optional per-part overrides; when NULL the
-- part falls back to the design's template file and material/colour.
CREATE TABLE IF NOT EXISTS design_parts (
    id             uuid PRIMARY KEY,
    design_id      uuid NOT NULL REFERENCES designs (id) ON DELETE CASCADE,
    role           varchar(64) NOT NULL,
    part_index     integer NOT NULL DEFAULT 0,
    quantity       integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    print_file_id  uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    material       varchar(255),
    colour         varchar(255),
    nozzle_profile varchar(255),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS design_parts_design_role_key ON design_parts (design_id, role);
CREATE INDEX IF NOT EXISTS ix_design_parts_design ON design_parts (design_id, part_index);

-- +goose Down
DROP TABLE IF EXISTS design_parts;
