-- +goose Up
-- A ready-made plate belongs to a SKU, not to a component.
--
-- 0118 kept one plate per component per machine class, which is right only if
-- the part is the same whatever it goes on. It is not. The sliced file has the
-- filament baked into it, so a plate of purple diya cups is not a plate of red
-- ones - and the Royal sells in purple, red and green. Three SKUs, three
-- machine classes, nine files.
--
-- Keyed on the SKU STRING rather than product_variants.id, exactly as
-- sku_slicer_pipelines is and for the reason migration 0085 gives: a mapping
-- hung off variant_id reaches almost nothing that actually prints, because the
-- SKUs on the floor are ahead of the registry. A component may be declared
-- against a Shopify product that nobody has imported yet.
ALTER TABLE component_plates
    ADD COLUMN IF NOT EXISTS sku varchar(128) NOT NULL DEFAULT '';

DROP INDEX IF EXISTS uq_component_plate_family;

CREATE UNIQUE INDEX IF NOT EXISTS uq_component_plate_sku_family
    ON component_plates (inventory_item_id, lower(sku), upper(machine_family));

-- THE FILE IS THE WHOLE ANSWER, and the rest was ceremony.
--
-- 0118 asked for the material, the colour, the pipeline and how many came off
-- the plate, so a screen could warn that a stored plate had gone stale. The
-- shop's answer is that the file for a SKU on a machine is simply the file:
-- the colour is the SKU's own, and nothing else has to be re-entered to
-- describe a thing somebody already sliced. Four fields nobody fills are four
-- fields that go wrong.
ALTER TABLE component_plates DROP COLUMN IF EXISTS units_per_plate;
ALTER TABLE component_plates DROP COLUMN IF EXISTS pipeline_name;
ALTER TABLE component_plates DROP COLUMN IF EXISTS material;
ALTER TABLE component_plates DROP COLUMN IF EXISTS colour;
ALTER TABLE component_plates DROP CONSTRAINT IF EXISTS ck_component_plate_units;

-- Which Shopify product a component is a part of.
--
-- The component is named by choosing that product, and its SKUs are what the
-- plate rows hang off. The gid is stored rather than a products(id) FK because
-- the picker lists the LIVE Shopify catalogue, which is wider than the
-- registry - a part can be declared for a product before anybody imports it.
ALTER TABLE inventory_items
    ADD COLUMN IF NOT EXISTS shopify_product_gid varchar(64);

-- +goose Down
ALTER TABLE inventory_items DROP COLUMN IF EXISTS shopify_product_gid;

DROP INDEX IF EXISTS uq_component_plate_sku_family;
ALTER TABLE component_plates DROP COLUMN IF EXISTS sku;
ALTER TABLE component_plates ADD COLUMN IF NOT EXISTS units_per_plate integer NOT NULL DEFAULT 1;
ALTER TABLE component_plates ADD COLUMN IF NOT EXISTS pipeline_name varchar(200);
ALTER TABLE component_plates ADD COLUMN IF NOT EXISTS material varchar(20);
ALTER TABLE component_plates ADD COLUMN IF NOT EXISTS colour varchar(60);

CREATE UNIQUE INDEX IF NOT EXISTS uq_component_plate_family
    ON component_plates (inventory_item_id, upper(machine_family));
