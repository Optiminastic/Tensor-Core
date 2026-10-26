-- +goose Up
-- A component may be MADE HERE rather than bought in.
--
-- A product is not one printed thing. The diya holder that attaches to PDHR,
-- the red heart on a plank, the black box a lit plank sits in - each is
-- printed separately, and none of them carries the customer's name.
--
-- The personalised halves already have a home: a design role with a field
-- mapping becomes a production job per order, gets batched and queued. These
-- have had none, and the two ways of faking it are both wrong.
--
-- A design role with NO field mapping silently makes no job, because
-- partRolesForRender returns only roles that have a mapping. That is the
-- behaviour wanted, and nobody wrote it down: the day somebody adds a mapping
-- to that role it starts making a job per order with no warning. It also
-- carries no quantity and no plate.
--
-- A bill-of-materials line is counted and costed, but inventory_items has no
-- link to a design file - so nothing knows the part is made by printing.
--
-- This is the second. An inventory item rather than a design role because a
-- red heart is shared by every colour of plank and by several products: an
-- item is shared, a role belongs to one product.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS source varchar(16) NOT NULL DEFAULT 'bought';

-- TWO link kinds, because a component can be either thing.
--
-- A red heart is a fixed mesh with no variables, so it uploads as its own
-- .stl or .3mf - and it cannot go through design_templates, whose upload
-- refuses anything but .scad. A component that IS parameterised points at a
-- template key instead.
--
-- Both are global on purpose. inventory_items has no brand, and neither has
-- design_templates.template_key nor file_assets. The `designs` table IS brand
-- scoped, so linking a shared shelf item to one would let a component point at
-- another brand's file - which is why design_id is not an option here.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS template_key varchar(64);

ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS model_file_id uuid REFERENCES file_assets (id) ON DELETE RESTRICT;

ALTER TABLE inventory_items
    DROP CONSTRAINT IF EXISTS ck_inventory_item_source;

ALTER TABLE inventory_items
    ADD CONSTRAINT ck_inventory_item_source CHECK (source IN ('bought', 'printed'));

-- Shaped like ck_variant_design_target, and for the same reason: a row naming
-- both files is ambiguous about which one prints, and one naming neither
-- claims to be printed here with nothing to print.
ALTER TABLE inventory_items
    DROP CONSTRAINT IF EXISTS ck_inventory_item_made;

ALTER TABLE inventory_items
    ADD CONSTRAINT ck_inventory_item_made CHECK (
        (source = 'bought' AND template_key IS NULL AND model_file_id IS NULL)
        OR (source = 'printed' AND num_nonnulls(template_key, model_file_id) = 1)
    );

-- +goose Down
ALTER TABLE inventory_items DROP CONSTRAINT IF EXISTS ck_inventory_item_made;
ALTER TABLE inventory_items DROP CONSTRAINT IF EXISTS ck_inventory_item_source;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS model_file_id;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS template_key;
ALTER TABLE inventory_items DROP COLUMN IF EXISTS source;
