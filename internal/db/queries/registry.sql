-- name: ListProducts :many
-- Every product, with how much is defined about it.
--
-- The counts are what the list is FOR: a product with options but no variants,
-- or variants with no bill of materials, is half-registered, and that is the
-- thing somebody opening this page needs to see without clicking into each one.
SELECT p.*,
       (SELECT count(*) FROM product_options o WHERE o.product_id = p.id)  AS option_count,
       (SELECT count(*) FROM product_variants v WHERE v.product_id = p.id) AS variant_count
FROM products p
ORDER BY lower(p.code);

-- name: GetProductByCode :one
SELECT * FROM products WHERE lower(code) = lower(sqlc.arg('code')::text);

-- name: InsertProduct :one
INSERT INTO products (id, code, name, kind, status, notes)
VALUES (sqlc.arg('id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('kind'), sqlc.arg('status'), sqlc.narg('notes'))
RETURNING *;

-- name: ListProductOptions :many
-- The option axes of one product, in display order.
SELECT * FROM product_options
WHERE product_id = sqlc.arg('product_id')
ORDER BY position, lower(code);

-- name: InsertProductOption :one
INSERT INTO product_options (id, product_id, code, label, position)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('code'),
        sqlc.arg('label'), sqlc.arg('position'))
RETURNING *;

-- name: ListOptionValues :many
-- Every allowed value across one product's options, so the page can render the
-- axes in one read rather than one per option.
SELECT v.*, o.product_id, o.code AS option_code
FROM product_option_values v
JOIN product_options o ON o.id = v.option_id
WHERE o.product_id = sqlc.arg('product_id')
ORDER BY o.position, v.position, lower(v.code);

-- name: InsertOptionValue :one
INSERT INTO product_option_values (id, option_id, code, label, position)
VALUES (sqlc.arg('id'), sqlc.arg('option_id'), sqlc.arg('code'),
        sqlc.arg('label'), sqlc.arg('position'))
RETURNING *;

-- name: ListVariantsForProduct :many
-- One product's variants, with what each resolves to.
--
-- option_codes is "heart_count=2,light=wired" rather than a join the caller has
-- to reassemble: a variant IS its option values, and a list that made the page
-- fetch them separately per row would be one query per variant.
SELECT v.*,
       (SELECT string_agg(o.code || '=' || ov.code, ',' ORDER BY o.position, o.code)
          FROM product_variant_options pvo
          JOIN product_option_values ov ON ov.id = pvo.option_value_id
          JOIN product_options o ON o.id = ov.option_id
         WHERE pvo.variant_id = v.id) AS option_codes,
       (SELECT count(*) FROM variant_bom b WHERE b.variant_id = v.id) AS part_count
FROM product_variants v
WHERE v.product_id = sqlc.arg('product_id')
ORDER BY lower(v.name);

-- name: InsertVariant :one
INSERT INTO product_variants (id, product_id, sku, name, status)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.narg('sku'),
        sqlc.arg('name'), sqlc.arg('status'))
RETURNING *;

-- name: AddVariantOptionValue :exec
-- Ties a variant to one of its option values. Idempotent so re-seeding a
-- registry does not fail halfway through on a row that already exists.
INSERT INTO product_variant_options (variant_id, option_value_id)
VALUES (sqlc.arg('variant_id'), sqlc.arg('option_value_id'))
ON CONFLICT DO NOTHING;

-- name: ListVariantDesigns :many
SELECT * FROM variant_designs
WHERE variant_id = sqlc.arg('variant_id') AND status = 'active'
ORDER BY role;

-- name: InsertVariantDesign :one
INSERT INTO variant_designs (id, variant_id, role, template_key, design_id, version, status)
VALUES (sqlc.arg('id'), sqlc.arg('variant_id'), sqlc.arg('role'),
        sqlc.narg('template_key'), sqlc.narg('design_id'),
        sqlc.arg('version'), sqlc.arg('status'))
RETURNING *;

-- name: ListVariantBom :many
-- A variant's parts, joined to the shelf so the page can price them.
--
-- unit_price comes from inventory_items rather than being copied here: a BOM
-- says how MANY, the shelf says what one COSTS, and duplicating the price would
-- mean every supplier change had to be applied in two places.
SELECT b.*, i.name AS item_name, i.code AS item_code, i.unit,
       i.unit_price, i.quantity AS stock_quantity
FROM variant_bom b
JOIN inventory_items i ON i.id = b.inventory_item_id
WHERE b.variant_id = sqlc.arg('variant_id')
ORDER BY lower(i.name);

-- name: ReplaceVariantBomItem :exec
-- Adds a part to a bill of materials, or corrects its quantity.
INSERT INTO variant_bom (id, variant_id, inventory_item_id, quantity)
VALUES (sqlc.arg('id'), sqlc.arg('variant_id'), sqlc.arg('inventory_item_id'),
        sqlc.arg('quantity')::float8)
ON CONFLICT (variant_id, inventory_item_id) DO UPDATE
SET quantity = EXCLUDED.quantity;

-- name: ClearVariantBom :exec
-- Empties a bill of materials so it can be written whole.
--
-- The BOM is edited as a LIST and saved once, so a half-applied edit cannot
-- leave a variant with a battery and no switch. Paired with
-- ReplaceVariantBomItem inside one transaction.
DELETE FROM variant_bom WHERE variant_id = sqlc.arg('variant_id');

-- name: ListBomForProduct :many
-- Every bill-of-materials line across one product's variants, in one read.
--
-- One query rather than one per variant: the Products page shows a product and
-- all its variants at once, and a request per variant would be six round trips
-- to render one panel - growing with every colour the shop adds.
SELECT b.variant_id, b.quantity, i.id AS item_id, i.name AS item_name,
       i.code AS item_code, i.unit, i.unit_price
FROM variant_bom b
JOIN inventory_items i ON i.id = b.inventory_item_id
JOIN product_variants v ON v.id = b.variant_id
WHERE v.product_id = sqlc.arg('product_id')
ORDER BY lower(i.name);

-- name: ListDesignsForProduct :many
-- Which file each of a product's variants prints from, in one read, for the
-- same reason as ListBomForProduct.
SELECT d.variant_id, v.sku, d.role, d.template_key, d.design_id, d.version
FROM variant_designs d
JOIN product_variants v ON v.id = d.variant_id
WHERE v.product_id = sqlc.arg('product_id') AND d.status = 'active'
ORDER BY d.role;

-- name: UpdateProduct :one
-- Corrects what a product IS. The code is editable because it is data somebody
-- typed, not an identity: a product mis-registered as "DPN" must be fixable
-- without deleting the variants and bills of materials hanging off it.
UPDATE products
SET code = sqlc.arg('code'),
    name = sqlc.arg('name'),
    kind = sqlc.arg('kind'),
    status = sqlc.arg('status'),
    notes = sqlc.narg('notes'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteProduct :exec
-- Removes a product and, by cascade, its options, variants, designs and bills
-- of materials.
--
-- One statement rather than a careful unwind: every child FK is ON DELETE
-- CASCADE by design, so a hand-written unwind would only be a second, worse
-- copy of rules the schema already states. The caller is responsible for
-- telling somebody what is about to go - see the counts on ListProducts.
DELETE FROM products WHERE id = sqlc.arg('id');

-- name: UpdateProductOption :one
UPDATE product_options
SET code = sqlc.arg('code'), label = sqlc.arg('label'), position = sqlc.arg('position')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteProductOption :exec
-- Removes an axis and, by cascade, its values and every variant's use of them.
--
-- A variant whose axis is gone keeps existing, deliberately: it is still a real
-- combination somebody sold, and silently deleting sold variants to tidy up a
-- schema change would be the worse failure.
DELETE FROM product_options WHERE id = sqlc.arg('id');

-- name: UpdateOptionValue :one
UPDATE product_option_values
SET code = sqlc.arg('code'), label = sqlc.arg('label'), position = sqlc.arg('position')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteOptionValue :exec
DELETE FROM product_option_values WHERE id = sqlc.arg('id');

-- name: GetOptionValueProduct :one
-- The product an option value belongs to, so a caller cannot build a variant
-- out of another product's values.
SELECT o.product_id
FROM product_option_values v
JOIN product_options o ON o.id = v.option_id
WHERE v.id = sqlc.arg('id');

-- name: UpdateVariant :one
UPDATE product_variants
SET sku = sqlc.narg('sku'), name = sqlc.arg('name'),
    status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteVariant :exec
DELETE FROM product_variants WHERE id = sqlc.arg('id');

-- name: GetVariant :one
SELECT * FROM product_variants WHERE id = sqlc.arg('id');

-- name: ClearVariantOptions :exec
-- Empties a variant's option values so the set can be written whole, the same
-- way a bill of materials is. A half-applied change would leave a variant that
-- is two option values at once.
DELETE FROM product_variant_options WHERE variant_id = sqlc.arg('variant_id');

-- name: SupersedeVariantDesign :exec
-- Retires whatever currently prints this role, so the new row can take the
-- partial unique index on (variant_id, role) WHERE status = 'active'.
--
-- Retired rather than deleted: "which file printed the planks we shipped last
-- Tuesday" must stay answerable after somebody changes it.
UPDATE variant_designs
SET status = 'replaced'
WHERE variant_id = sqlc.arg('variant_id')
  AND role = sqlc.arg('role')
  AND status = 'active';

-- name: ListProductFieldMaps :many
-- One design file's order-field-to-OpenSCAD-variable mapping, in editor order.
--
-- Read once per model generated to build the -D flags, and read again by the
-- editor that maintains it. Ordered by position then variable so the list
-- reads the same way twice and a diff between two products is meaningful.
--
-- Scoped to a role, because a product may print from more than one .scad and
-- the rose's mapping is not the keychain's.
SELECT * FROM product_field_maps
WHERE product_id = sqlc.arg('product_id')
  AND role = sqlc.arg('role')
  AND variant_id IS NULL
ORDER BY position, lower(scad_variable);

-- name: ListAllProductFieldMaps :many
-- Every mapped field of a product, across all of its design files.
--
-- For the editor and for counting what a product has configured. The render
-- path uses ListProductFieldMaps, which asks about the one file it is about
-- to run.
SELECT * FROM product_field_maps
WHERE product_id = sqlc.arg('product_id')
ORDER BY role, position, lower(scad_variable);

-- name: InsertProductFieldMap :exec
-- Adds one mapped field.
--
-- ON CONFLICT rather than a blind insert: the unique index is on
-- (product_id, role, lower(scad_variable)) because two rows naming NAME_L for
-- one file would both become -D flags and OpenSCAD takes the last on the
-- command line - so the model would depend on iteration order, which is to
-- say on nothing. Across two files they are different variables in different
-- scripts and both are kept.
INSERT INTO product_field_maps (
    id, product_id, property_key, scad_variable, value_type, role, required,
    fixed_value, default_value, max_runes, position
) VALUES (
    sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('property_key'),
    sqlc.arg('scad_variable'), sqlc.arg('value_type'), sqlc.arg('role'),
    sqlc.arg('required'), sqlc.narg('fixed_value'), sqlc.narg('default_value'),
    sqlc.narg('max_runes'), sqlc.arg('position')
)
-- The WHERE is NOT optional. uq_product_field_map_variable became partial
-- in migration 0129, when a variant gained the right to override a
-- product's mapping, and Postgres matches an ON CONFLICT target against a
-- partial index only when the predicate is repeated. Without it every call
-- fails with 42P10 - "no unique or exclusion constraint matching the ON
-- CONFLICT specification" - which names neither the index nor the reason.
ON CONFLICT (product_id, role, lower(scad_variable)) WHERE variant_id IS NULL DO UPDATE
SET property_key  = EXCLUDED.property_key,
    value_type    = EXCLUDED.value_type,
    required      = EXCLUDED.required,
    fixed_value   = EXCLUDED.fixed_value,
    default_value = EXCLUDED.default_value,
    max_runes     = EXCLUDED.max_runes,
    position      = EXCLUDED.position;

-- name: ClearProductFieldMaps :exec
-- Empties ONE design file's mapping so it can be written whole.
--
-- Same reasoning as ClearVariantBom: the mapping is edited as a LIST and saved
-- once, so a half-applied edit cannot leave a product mapping a first name and
-- not a second. Paired with InsertProductFieldMap inside one transaction.
--
-- Scoped to the role being saved, or editing the rose would silently delete
-- the keychain's mapping.
DELETE FROM product_field_maps
WHERE product_id = sqlc.arg('product_id')
  AND role = sqlc.arg('role');

-- name: FindProductBySKU :one
-- The product an order line belongs to, found by the SKU on its variant.
--
-- This is the lookup that makes the registry drive rendering: an order carries
-- a SKU, product_variants carries the same SKU, and the product it hangs off
-- owns the template and the field mapping.
--
-- Case-insensitive, because the storefront is not consistent about it and a
-- SKU that matched only in upper case would silently fall through to the
-- hardcoded path - a product configured in the registry that quietly ignores
-- its configuration.
--
-- Retired variants are excluded: a SKU that has been withdrawn should stop
-- rendering rather than keep printing from a configuration nobody maintains.
SELECT p.*
FROM product_variants v
JOIN products p ON p.id = v.product_id
WHERE lower(v.sku) = lower(sqlc.arg('sku'))
  AND v.status = 'active'
  AND p.status = 'active'
LIMIT 1;

-- name: FindVariantBySKU :one
-- The VARIANT an order line belongs to, found by its SKU.
--
-- FindProductBySKU's sibling, and matched on exactly the same terms so the two
-- cannot disagree about which SKUs the registry describes. The product answers
-- "which template and which field mapping"; the variant answers "which
-- colourway", which is a different question as soon as a product's palette
-- changes between variants.
SELECT v.*
FROM product_variants v
JOIN products p ON p.id = v.product_id
WHERE lower(v.sku) = lower(sqlc.arg('sku'))
  AND v.status = 'active'
  AND p.status = 'active'
LIMIT 1;

-- name: UpsertProductByCode :one
-- Creates a product or refreshes its name, for an import that may be a re-import.
--
-- Kind and status are NOT overwritten on conflict. A re-import is "Shopify has
-- new variants", not "undo whatever somebody decided about this product here" -
-- retiring a product and then re-importing it to pick up a colour should not
-- quietly bring it back to life.
INSERT INTO products (id, code, name, kind, status, notes)
VALUES (sqlc.arg('id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('kind'), sqlc.arg('status'), sqlc.narg('notes'))
-- lower(code), because that is what uq_products_code is on. A conflict target
-- of (code) names no index Postgres can infer and fails the whole import with
-- SQLSTATE 42P10 - which it did, on the first real product anybody tried.
ON CONFLICT (lower(code)) DO UPDATE
SET name       = EXCLUDED.name,
    updated_at = now()
RETURNING *;

-- name: UpsertVariantBySKU :exec
-- Creates a variant or refreshes it, keyed on the SKU.
--
-- The SKU is the identity: it is what an order carries and what
-- FindProductBySKU matches on, so a variant renamed in Shopify is the same
-- variant and a variant given a new SKU is a new one.
--
-- Sets status back to 'active', because a re-import is the statement that
-- Shopify still sells this - which is exactly how a variant retired by an
-- earlier import comes back when the shop restores it.
INSERT INTO product_variants (id, product_id, sku, name, status)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('sku'),
        sqlc.arg('name'), 'active')
ON CONFLICT (lower(sku)) WHERE sku IS NOT NULL DO UPDATE
SET product_id = EXCLUDED.product_id,
    name       = EXCLUDED.name,
    status     = 'active',
    updated_at = now();

-- name: RetireVariantsNotInSKUs :execrows
-- Retires a product's variants that the import no longer saw.
--
-- Retired, never deleted. A withdrawn SKU must stop matching NEW orders, but
-- jobs and beds already reference the variant it belonged to, and deleting it
-- would orphan them to tidy a list. FindProductBySKU already ignores anything
-- not active, so retiring is the whole of the behaviour change.
--
-- Variants with no SKU are retired too: they cannot be matched to an order and
-- an import is the moment somebody is looking at what this product covers.
UPDATE product_variants
SET status = 'retired', updated_at = now()
WHERE product_id = sqlc.arg('product_id')
  AND status <> 'retired'
  AND (sku IS NULL OR lower(sku) <> ALL (sqlc.arg('skus')::text[]));

-- name: ListSKUPipelines :many
-- Every SKU that prints, with its slicer-pipeline mapping.
--
-- A union of three places a SKU can come from, because no single one of them
-- knows every SKU the shop actually prints: the registry's variants, the jobs
-- themselves, and the mapping table. The live floor is the reason - of eight
-- SKUs printing today, one has a matching variant row, so a list built from the
-- registry alone would offer almost nothing worth mapping.
--
-- Mapped and unmapped alike: "not mapped yet" is the state somebody opens this
-- page to fix, and omitting those rows would hide exactly the work to be done.
WITH known AS (
    SELECT lower(v.sku)::text AS sku, v.sku AS display, p.code AS product_code,
           p.name AS product_name
    FROM product_variants v
    JOIN products p ON p.id = v.product_id
    WHERE v.sku IS NOT NULL AND v.status <> 'retired' AND p.status <> 'retired'
    UNION
    SELECT lower(j.sku)::text, j.sku, ''::varchar, coalesce(j.product_name, '')::varchar
    FROM production_jobs j
    WHERE j.sku IS NOT NULL AND btrim(j.sku) <> ''
    UNION
    SELECT lower(sp.sku)::text, sp.sku, ''::varchar, ''::varchar
    FROM sku_slicer_pipelines sp
    -- A PRODUCT's mapping carries no SKU, and this list is of SKUs. Without
    -- this it arrives as a row whose sku is null - a phantom entry that is
    -- neither a product nor anything that prints.
    WHERE sp.sku IS NOT NULL
)
SELECT k.sku,
       max(k.display)::text      AS display_sku,
       max(k.product_code)::text AS product_code,
       max(k.product_name)::text AS product_name,
       sp.machine_family, sp.pipeline_id, sp.pipeline_name
FROM known k
LEFT JOIN sku_slicer_pipelines sp ON lower(sp.sku) = k.sku
GROUP BY k.sku, sp.machine_family, sp.pipeline_id, sp.pipeline_name
ORDER BY k.sku, sp.machine_family;

-- name: GetPipelineForSKU :one
-- The pipeline one SKU prints with on one machine class - the slice path's
-- lookup. Case-insensitive on both sides: a SKU typed into Shopify and one
-- typed into Tensor disagree about case more often than anybody expects.
SELECT * FROM sku_slicer_pipelines
WHERE sku IS NOT NULL
  AND lower(sku) = lower(sqlc.arg('sku')::text)
  AND upper(machine_family) = upper(sqlc.arg('machine_family')::text);

-- name: ListAllPipelineMappings :many
-- Every SKU-to-pipeline mapping on the floor, for building the batching token.
--
-- Read whole rather than per job: the compatibility key is computed in loops
-- over candidate pools that run to hundreds of jobs, and a query each would be
-- hundreds of round trips to answer a question this table can answer in one.
SELECT lower(sku)::text AS sku, machine_family, pipeline_id
FROM sku_slicer_pipelines
WHERE sku IS NOT NULL
ORDER BY lower(sku), upper(machine_family);

-- name: UpsertSKUPipeline :one
INSERT INTO sku_slicer_pipelines (id, sku, machine_family, pipeline_id, pipeline_name)
VALUES (
    sqlc.arg('id'), sqlc.arg('sku'), sqlc.arg('machine_family'),
    sqlc.arg('pipeline_id'), sqlc.arg('pipeline_name')
)
-- The WHERE repeats the index's, because uq_sku_pipeline_family is PARTIAL
-- now that a product's mapping carries no SKU. Postgres infers the arbiter
-- index from this clause and finds none without it.
ON CONFLICT (lower(sku), upper(machine_family)) WHERE sku IS NOT NULL DO UPDATE SET
    pipeline_id   = EXCLUDED.pipeline_id,
    pipeline_name = EXCLUDED.pipeline_name,
    updated_at    = now()
RETURNING *;

-- name: DeleteSKUPipeline :exec
-- Clearing a mapping returns the SKU to the class default, which is a real
-- choice and not an absence of one - hence a delete rather than a null.
DELETE FROM sku_slicer_pipelines
WHERE lower(sku) = lower(sqlc.arg('sku')::text)
  AND upper(machine_family) = upper(sqlc.arg('machine_family')::text);

-- name: ListSellableVariants :many
-- Every variant that carries a SKU, with its product's name and code.
--
-- A SKU is what a quotation line and a Shopify price are both keyed by, so a
-- variant without one cannot appear on either side and is excluded here rather
-- than filtered in Go.
SELECT v.id, v.sku, v.name AS variant_name, v.status,
       p.name AS product_name, p.code AS product_code
FROM product_variants v
JOIN products p ON p.id = v.product_id
WHERE v.sku IS NOT NULL AND btrim(v.sku) <> ''
  AND v.status = 'active' AND p.status = 'active'
ORDER BY p.name, v.name;

-- name: ListFieldMapsForProducts :many
-- Every order-fed mapping for a set of products, across all roles.
--
-- Order-fed only: a row with a fixed_value (OUT_X=200) is a constant the
-- renderer supplies, not something anybody can be asked for, so it must never
-- become a column somebody has to fill in.
--
-- All roles together, because a bulk-order sheet describes the whole product -
-- a Soulmate COMBO's plank, rose and keychain are one row for the customer even
-- though they are three design files to the renderer. The role is returned so
-- two rows naming the same variable (SC has NAME for both the rose and the
-- keychain) can be told apart.
SELECT f.*, p.code AS product_code, p.name AS product_name
FROM product_field_maps f
JOIN products p ON p.id = f.product_id
WHERE f.product_id = ANY(sqlc.arg('product_ids')::uuid[])
  AND f.fixed_value IS NULL
  AND btrim(f.property_key) <> ''
ORDER BY p.code, f.position, lower(f.scad_variable);

-- name: GetVariantProduct :one
-- Which product a variant belongs to, by variant id.
SELECT v.id AS variant_id, v.sku, p.id AS product_id, p.code AS product_code, p.name AS product_name
FROM product_variants v JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

-- name: ListDesignColourParts :many
-- The coloured pieces one design file prints as, in the order they were read
-- from the reference 3MF.
--
-- The PRODUCT's scheme only - rows no variant has overridden. A render asks
-- ListColourPartsForVariant instead, which falls back to this.
--
-- Empty is the normal answer for every product configured before this existed,
-- and the renderer treats it as "use the two-pass plank behaviour" rather than
-- as a misconfiguration.
SELECT * FROM design_colour_parts
WHERE product_id = sqlc.arg('product_id')
  AND lower(role) = lower(sqlc.arg('role')::text)
  AND variant_id IS NULL
ORDER BY position, lower(part_name);

-- name: ListColourPartsForVariant :many
-- The pieces ONE VARIANT declares of its own, or none.
--
-- The fallback to the product's scheme is resolveScheme's, in Go, where the
-- rule it enforces can be tested and stated: a variant that declares a scheme
-- declares the WHOLE scheme. Expressed here as a UNION it was correct and
-- unreadable, and nothing could assert it without a database.
SELECT * FROM design_colour_parts
WHERE variant_id = sqlc.arg('variant_id')
  AND lower(role) = lower(sqlc.arg('role')::text)
ORDER BY position, lower(part_name);

-- name: ListAllDesignColourParts :many
-- Every coloured piece of one product, across all its roles AND variants, for
-- the editor. The SKU comes along so the editor can group by colourway without
-- a second query; it is NULL on the product's own scheme.
SELECT dcp.*, v.sku AS variant_sku
FROM design_colour_parts dcp
LEFT JOIN product_variants v ON v.id = dcp.variant_id
WHERE dcp.product_id = sqlc.arg('product_id')
ORDER BY lower(dcp.role), v.sku NULLS FIRST, dcp.position, lower(dcp.part_name);

-- name: ClearDesignColourParts :exec
-- Removes one role's pieces, so a re-uploaded reference 3MF replaces rather
-- than merges. A part that disappeared from the file has disappeared from the
-- product, and leaving it behind would render a piece the design no longer has.
DELETE FROM design_colour_parts
WHERE product_id = sqlc.arg('product_id')
  AND lower(role) = lower(sqlc.arg('role')::text)
  AND variant_id IS NOT DISTINCT FROM sqlc.narg('variant_id')::uuid;

-- name: InsertDesignColourPart :one
INSERT INTO design_colour_parts (id, product_id, role, part_name, colour_hex, position, variant_id)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('role'),
        sqlc.arg('part_name'), sqlc.narg('colour_hex'), sqlc.arg('position'),
        sqlc.narg('variant_id'))
RETURNING *;

-- name: SetDesignColourPartColour :one
-- Changes one piece between a fixed colour and the customer's choice.
--
-- NULL is the customer's choice, which is why this takes a nullable colour
-- rather than a colour and a flag: the two can contradict each other and
-- nothing here needs them to.
UPDATE design_colour_parts
SET colour_hex = sqlc.narg('colour_hex')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: CountSKUPipelinesTable :one
-- How many SKUs the SKU Pipelines panel's search selects - the same union of
-- variants, jobs and mappings ListSKUPipelines reads, one row per SKU.
WITH known AS (
    SELECT lower(v.sku)::text AS sku, v.sku AS display, p.code AS product_code,
           p.name AS product_name
    FROM product_variants v
    JOIN products p ON p.id = v.product_id
    WHERE v.sku IS NOT NULL AND v.status <> 'retired' AND p.status <> 'retired'
    UNION
    SELECT lower(j.sku)::text, j.sku, ''::varchar, coalesce(j.product_name, '')::varchar
    FROM production_jobs j
    WHERE j.sku IS NOT NULL AND btrim(j.sku) <> ''
    UNION
    SELECT lower(sp.sku)::text, sp.sku, ''::varchar, ''::varchar
    FROM sku_slicer_pipelines sp
    -- A PRODUCT's mapping carries no SKU, and this list is of SKUs. Without
    -- this it arrives as a row whose sku is null - a phantom entry that is
    -- neither a product nor anything that prints.
    WHERE sp.sku IS NOT NULL
),
skus AS (
    SELECT k.sku,
           max(k.display)::text      AS display_sku,
           max(k.product_code)::text AS product_code,
           max(k.product_name)::text AS product_name
    FROM known k
    GROUP BY k.sku
),
matching AS (
    -- The panel's search box, over the same three values it searched.
    SELECT * FROM skus s
    WHERE sqlc.narg('search')::text IS NULL
       OR s.display_sku ILIKE '%' || sqlc.narg('search')::text || '%'
       OR s.product_name ILIKE '%' || sqlc.narg('search')::text || '%'
       OR s.product_code ILIKE '%' || sqlc.narg('search')::text || '%'
)
SELECT count(*) FROM matching;

-- name: ListSKUPipelinesTable :many
-- One page of SKUs - cut by LIMIT/OFFSET over SKUs, not over mapping rows, so
-- a SKU's three families are never split across pages - with their mappings.
WITH known AS (
    SELECT lower(v.sku)::text AS sku, v.sku AS display, p.code AS product_code,
           p.name AS product_name
    FROM product_variants v
    JOIN products p ON p.id = v.product_id
    WHERE v.sku IS NOT NULL AND v.status <> 'retired' AND p.status <> 'retired'
    UNION
    SELECT lower(j.sku)::text, j.sku, ''::varchar, coalesce(j.product_name, '')::varchar
    FROM production_jobs j
    WHERE j.sku IS NOT NULL AND btrim(j.sku) <> ''
    UNION
    SELECT lower(sp.sku)::text, sp.sku, ''::varchar, ''::varchar
    FROM sku_slicer_pipelines sp
    -- A PRODUCT's mapping carries no SKU, and this list is of SKUs. Without
    -- this it arrives as a row whose sku is null - a phantom entry that is
    -- neither a product nor anything that prints.
    WHERE sp.sku IS NOT NULL
),
skus AS (
    SELECT k.sku,
           max(k.display)::text      AS display_sku,
           max(k.product_code)::text AS product_code,
           max(k.product_name)::text AS product_name
    FROM known k
    GROUP BY k.sku
),
matching AS (
    -- The panel's search box, over the same three values it searched.
    SELECT * FROM skus s
    WHERE sqlc.narg('search')::text IS NULL
       OR s.display_sku ILIKE '%' || sqlc.narg('search')::text || '%'
       OR s.product_name ILIKE '%' || sqlc.narg('search')::text || '%'
       OR s.product_code ILIKE '%' || sqlc.narg('search')::text || '%'
),
page AS (
    SELECT * FROM matching ORDER BY sku
    LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset')
)
SELECT p.sku, p.display_sku, p.product_code, p.product_name,
       sp.machine_family, sp.pipeline_id, sp.pipeline_name
FROM page p
LEFT JOIN sku_slicer_pipelines sp ON lower(sp.sku) = p.sku
ORDER BY p.sku, sp.machine_family;

-- name: CountVariantsForProduct :one
-- How many variants one product has, and how many parts they hold between
-- them - the figures the product card and its delete dialog state, without
-- reading the variants themselves.
SELECT count(*) AS variant_count,
       coalesce(sum((SELECT count(*) FROM variant_bom b WHERE b.variant_id = v.id)), 0)::bigint AS part_count
FROM product_variants v
WHERE v.product_id = sqlc.arg('product_id');

-- name: ListVariantsForProductPage :many
-- One page of a product's variants: ListVariantsForProduct's columns and order,
-- cut by LIMIT/OFFSET in Postgres.
SELECT v.*,
       (SELECT string_agg(o.code || '=' || ov.code, ',' ORDER BY o.position, o.code)
          FROM product_variant_options pvo
          JOIN product_option_values ov ON ov.id = pvo.option_value_id
          JOIN product_options o ON o.id = ov.option_id
         WHERE pvo.variant_id = v.id) AS option_codes,
       (SELECT count(*) FROM variant_bom b WHERE b.variant_id = v.id) AS part_count
FROM product_variants v
WHERE v.product_id = sqlc.arg('product_id')
ORDER BY lower(v.name), v.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ListBomForVariants :many
-- The bill-of-materials lines of just these variants - one page's worth -
-- with ListBomForProduct's columns.
SELECT b.variant_id, b.quantity, i.id AS item_id, i.name AS item_name,
       i.code AS item_code, i.unit, i.unit_price
FROM variant_bom b
JOIN inventory_items i ON i.id = b.inventory_item_id
WHERE b.variant_id = ANY(sqlc.arg('variant_ids')::uuid[])
ORDER BY lower(i.name);

-- name: ListProductBom :many
-- The bill of materials every variant of a product inherits.
--
-- The same columns ListVariantBom returns, so a caller folding the two does
-- not have to reshape one of them.
SELECT b.id, b.product_id, b.quantity, b.created_at,
       i.id AS item_id, i.name AS item_name, i.code AS item_code, i.unit,
       i.unit_price, i.quantity AS stock_quantity, i.source
FROM product_bom b
JOIN inventory_items i ON i.id = b.inventory_item_id
WHERE b.product_id = sqlc.arg('product_id')
ORDER BY lower(i.name);

-- name: ReplaceProductBomItem :exec
INSERT INTO product_bom (id, product_id, inventory_item_id, quantity)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('inventory_item_id'),
        sqlc.arg('quantity')::float8)
ON CONFLICT (product_id, inventory_item_id) DO UPDATE
SET quantity = EXCLUDED.quantity;

-- name: ClearProductBom :exec
-- Empties a product's bill of materials so it can be written whole, exactly as
-- ClearVariantBom does. Paired with ReplaceProductBomItem inside one
-- transaction.
DELETE FROM product_bom WHERE product_id = sqlc.arg('product_id');

-- name: ListProductBomForProducts :many
-- Every product-level line for a set of products, for the list page.
SELECT b.product_id, b.quantity, i.id AS item_id, i.name AS item_name,
       i.code AS item_code, i.unit, i.unit_price
FROM product_bom b
JOIN inventory_items i ON i.id = b.inventory_item_id
WHERE b.product_id = ANY (sqlc.arg('product_ids')::uuid[])
ORDER BY lower(i.name);

-- name: ListVariantsWithOwnBom :many
-- Which variants of a product declare a bill of materials of their own.
--
-- Asked because inheritance is ALL OR NOTHING: a variant with any line of its
-- own uses only its own, so "does it have any" is the whole question and
-- counting them would invite a caller that merges.
SELECT DISTINCT b.variant_id
FROM variant_bom b
JOIN product_variants v ON v.id = b.variant_id
WHERE v.product_id = sqlc.arg('product_id');

-- name: SetInventoryItemSource :one
-- Declares how a component is made, and what prints it when the shop does.
--
-- Separate from UpdateInventoryItem because that one is the shelf - a name, a
-- count, a price - and is guarded by filament:manage, the permission for
-- loading spools. Binding a design file to a part is a configuration act.
UPDATE inventory_items SET
    source              = sqlc.arg('source'),
    template_key        = sqlc.narg('template_key'),
    model_file_id       = sqlc.narg('model_file_id'),
    shopify_product_gid = coalesce(sqlc.narg('shopify_product_gid'), shopify_product_gid),
    plate_scope         = coalesce(sqlc.narg('plate_scope'), plate_scope),
    shopify_product_title = coalesce(sqlc.narg('shopify_product_title'), shopify_product_title),
    updated_at          = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListComponentPlates :many
-- Every ready-made plate a component carries: one per SKU per machine class.
SELECT p.*, f.filename, f.size_bytes, f.storage_key
FROM component_plates p
JOIN file_assets f ON f.id = p.file_id
WHERE p.inventory_item_id = sqlc.arg('inventory_item_id')
ORDER BY lower(p.sku), upper(p.machine_family);

-- name: UpsertComponentPlate :one
-- The file IS the whole answer: one SKU, one machine class, one plate.
INSERT INTO component_plates (
    id, inventory_item_id, sku, machine_family, file_id, notes, uploaded_by
) VALUES (
    sqlc.arg('id'), sqlc.arg('inventory_item_id'), sqlc.arg('sku'),
    sqlc.arg('machine_family'), sqlc.arg('file_id'), sqlc.narg('notes'),
    sqlc.arg('uploaded_by')
)
ON CONFLICT (inventory_item_id, lower(sku), upper(machine_family)) DO UPDATE SET
    file_id     = EXCLUDED.file_id,
    notes       = EXCLUDED.notes,
    uploaded_by = EXCLUDED.uploaded_by,
    updated_at  = now()
RETURNING *;

-- name: DeleteComponentPlate :execrows
-- Removing a plate returns the component to being sliced per run, which is a
-- real choice and not an absence of one - hence a delete rather than a null.
DELETE FROM component_plates
WHERE inventory_item_id = sqlc.arg('inventory_item_id')
  AND lower(sku) = lower(sqlc.arg('sku')::text)
  AND upper(machine_family) = upper(sqlc.arg('machine_family')::text);

-- name: CountPlatesForItems :many
-- How many plates each component carries, for the Components table.
SELECT inventory_item_id, count(*)::int AS plates
FROM component_plates
WHERE inventory_item_id = ANY (sqlc.arg('item_ids')::uuid[])
GROUP BY inventory_item_id;

-- name: ListProductPipelines :many
-- Every registry product with its slicer mapping, one row per product per
-- machine class it has been given.
--
-- The PARENT is what gets assigned: the Lotus sells in three colourways and
-- the plank will have forty-four, and every one prints with the same settings.
-- A mapping per SKU is the same answer typed out once per colour.
SELECT p.id AS product_id, p.code AS product_code, p.name AS product_name,
       (SELECT count(*) FROM product_variants v
         WHERE v.product_id = p.id AND v.status <> 'retired'
           AND v.sku IS NOT NULL AND btrim(v.sku) <> '')::int AS sku_count,
       sp.machine_family, sp.pipeline_id, sp.pipeline_name
FROM products p
LEFT JOIN sku_slicer_pipelines sp ON sp.product_id = p.id
WHERE p.status <> 'retired'
ORDER BY lower(p.code), upper(sp.machine_family);

-- name: ListOrphanSKUPipelines :many
-- SKUs that print but belong to no registry product, with their own mappings.
--
-- They cannot be reached by assigning a parent, because they have none: of the
-- SKUs printing on this floor, most match no variant row. Listing them
-- individually is the only way they can be mapped at all, and dropping them
-- would quietly un-map the highest-volume product in the shop.
WITH printing AS (
    SELECT DISTINCT lower(j.sku)::text AS sku, j.sku AS display
    FROM production_jobs j
    WHERE j.sku IS NOT NULL AND btrim(j.sku) <> ''
      AND NOT EXISTS (
          SELECT 1 FROM product_variants v
          JOIN products p ON p.id = v.product_id
          WHERE lower(v.sku) = lower(j.sku) AND v.status <> 'retired' AND p.status <> 'retired'
      )
    UNION
    SELECT DISTINCT lower(sp.sku)::text, sp.sku
    FROM sku_slicer_pipelines sp
    WHERE sp.sku IS NOT NULL
)
SELECT k.sku, max(k.display)::text AS display_sku,
       sp.machine_family, sp.pipeline_id, sp.pipeline_name
FROM printing k
LEFT JOIN sku_slicer_pipelines sp ON lower(sp.sku) = k.sku
GROUP BY k.sku, sp.machine_family, sp.pipeline_id, sp.pipeline_name
ORDER BY k.sku, upper(sp.machine_family);

-- name: UpsertProductPipeline :one
INSERT INTO sku_slicer_pipelines (id, product_id, machine_family, pipeline_id, pipeline_name)
VALUES (sqlc.arg('id'), sqlc.arg('product_id'), sqlc.arg('machine_family'),
        sqlc.arg('pipeline_id'), sqlc.arg('pipeline_name'))
ON CONFLICT (product_id, upper(machine_family)) WHERE product_id IS NOT NULL DO UPDATE SET
    pipeline_id   = EXCLUDED.pipeline_id,
    pipeline_name = EXCLUDED.pipeline_name,
    updated_at    = now()
RETURNING *;

-- name: DeleteProductPipeline :exec
DELETE FROM sku_slicer_pipelines
WHERE product_id = sqlc.arg('product_id')
  AND upper(machine_family) = upper(sqlc.arg('machine_family')::text);

-- name: GetPipelineForProductOfSKU :one
-- The pipeline a SKU inherits from the product it belongs to.
--
-- Asked only when the SKU has no rule of its own, so the common path stays one
-- query. resolvePipeline is where the preference between the two lives.
SELECT sp.* FROM sku_slicer_pipelines sp
JOIN products p ON p.id = sp.product_id
JOIN product_variants v ON v.product_id = p.id
WHERE lower(v.sku) = lower(sqlc.arg('sku')::text)
  AND v.status = 'active' AND p.status = 'active'
  AND upper(sp.machine_family) = upper(sqlc.arg('machine_family')::text)
LIMIT 1;

-- name: ListShopifyProductPipelines :many
-- Every Shopify product that actually prints, with its slicer mapping.
--
-- THE PARENT EVERY SKU ALREADY HAS. The plank's twenty variants sit under one
-- Shopify product; the registry knows about two products in all. Deriving the
-- parent from order lines rather than from the registry means the list covers
-- what the floor prints today, not what has been imported.
--
-- product_name is the LINE's name - "Dual Name Plank - BLUE / NO LIGHT" - so
-- the shortest one under a product id is the closest thing to the parent's own
-- title without asking Shopify for it.
WITH lines AS (
    SELECT DISTINCT
           (li->>'product_id')::text AS shopify_product_id,
           lower(li->>'sku')::text AS sku,
           li->>'sku' AS display_sku,
           coalesce(li->>'product_name', '') AS line_name
    FROM orders o, jsonb_array_elements(o.line_items) li
    WHERE li->>'product_id' IS NOT NULL
      AND li->>'sku' IS NOT NULL AND btrim(li->>'sku') <> ''
),
printing AS (
    SELECT l.* FROM lines l
    WHERE EXISTS (SELECT 1 FROM production_jobs j WHERE lower(j.sku) = l.sku)
),
parents AS (
    SELECT shopify_product_id,
           count(DISTINCT sku)::int AS sku_count,
           (array_agg(line_name ORDER BY length(line_name), line_name))[1] AS name
    FROM printing
    GROUP BY shopify_product_id
)
SELECT p.shopify_product_id::text AS shopify_product_id,
       p.name::text AS name, p.sku_count,
       sp.machine_family, sp.pipeline_id, sp.pipeline_name
FROM parents p
LEFT JOIN sku_slicer_pipelines sp ON sp.shopify_product_id = p.shopify_product_id
ORDER BY lower(p.name), upper(sp.machine_family);

-- name: UpsertShopifyProductPipeline :one
INSERT INTO sku_slicer_pipelines (id, shopify_product_id, machine_family, pipeline_id, pipeline_name)
VALUES (sqlc.arg('id'), sqlc.arg('shopify_product_id'), sqlc.arg('machine_family'),
        sqlc.arg('pipeline_id'), sqlc.arg('pipeline_name'))
ON CONFLICT (shopify_product_id, upper(machine_family))
    WHERE shopify_product_id IS NOT NULL DO UPDATE SET
    pipeline_id   = EXCLUDED.pipeline_id,
    pipeline_name = EXCLUDED.pipeline_name,
    updated_at    = now()
RETURNING *;

-- name: DeleteShopifyProductPipeline :exec
DELETE FROM sku_slicer_pipelines
WHERE shopify_product_id = sqlc.arg('shopify_product_id')
  AND upper(machine_family) = upper(sqlc.arg('machine_family')::text);

-- name: GetPipelineForShopifyProductOfSKU :one
-- The pipeline a SKU inherits from the Shopify product it was sold under.
--
-- Asked last, and only when the SKU has no rule of its own and belongs to no
-- registry product - so the hot path pays for it once per unmapped job, never
-- for one already answered.
SELECT sp.* FROM sku_slicer_pipelines sp
WHERE sp.shopify_product_id IS NOT NULL
  AND upper(sp.machine_family) = upper(sqlc.arg('machine_family')::text)
  AND sp.shopify_product_id = (
      SELECT li->>'product_id'
      FROM orders o, jsonb_array_elements(o.line_items) li
      WHERE lower(li->>'sku') = lower(sqlc.arg('sku')::text)
        AND li->>'product_id' IS NOT NULL
      ORDER BY o.placed_at DESC NULLS LAST
      LIMIT 1
  )
LIMIT 1;

-- name: ListPreviewComponentsForSKU :many
-- The printed pieces that belong in a SKU's assembled picture.
--
-- Every component the SKU's product carries that Tensor can draw: printed
-- here, with a mesh of its own. A bought-in part has no model and a part with
-- only a .scad template is not a fixed mesh, so neither is returned - the
-- picture shows what can actually be drawn rather than a gap where a box
-- should be.
--
-- Through the bill of materials, so the pieces follow whatever the product is
-- actually built from. A variant's own list wins where it has one, exactly as
-- resolveBom decides it.
SELECT i.id, i.name, i.model_file_id, i.preview_colour, i.preview_position,
       f.storage_key, f.filename
FROM inventory_items i
JOIN file_assets f ON f.id = i.model_file_id
WHERE i.source = 'printed'
  AND i.model_file_id IS NOT NULL
  AND (
      i.id IN (
          SELECT b.inventory_item_id FROM variant_bom b
          JOIN product_variants v ON v.id = b.variant_id
          WHERE lower(v.sku) = lower(sqlc.arg('sku')::text)
      )
      OR (
          NOT EXISTS (
              SELECT 1 FROM variant_bom b
              JOIN product_variants v ON v.id = b.variant_id
              WHERE lower(v.sku) = lower(sqlc.arg('sku')::text)
          )
          AND i.id IN (
              SELECT pb.inventory_item_id FROM product_bom pb
              JOIN products p ON p.id = pb.product_id
              JOIN product_variants v ON v.product_id = p.id
              WHERE lower(v.sku) = lower(sqlc.arg('sku')::text)
          )
      )
  )
ORDER BY i.preview_position, lower(i.name);

-- name: AnySKUAsksTheCustomer :one
-- Whether ANY of these SKUs still asks its customer to confirm.
--
-- An order is the unit a link is sent for, so one enabled SKU on it is enough
-- to open a window. A SKU the registry has never seen counts as enabled: the
-- plank range is not in the registry at all and must keep behaving exactly as
-- it does today.
SELECT EXISTS (
    SELECT 1 FROM product_variants v
    WHERE lower(v.sku) = ANY (sqlc.arg('skus')::text[])
      AND v.personalisation_link_enabled
) OR NOT EXISTS (
    SELECT 1 FROM product_variants v
    WHERE lower(v.sku) = ANY (sqlc.arg('skus')::text[])
) AS asks;

-- name: ListVariantFieldMaps :many
-- The mapping ONE colourway declares of its own, or none.
--
-- The fallback to the product's is in Go, beside resolveScheme, where the
-- rule it enforces can be stated and tested: a variant that maps anything
-- maps everything. Merging the two would fill a gap in a variant's mapping
-- from the product's and drive a file with a variable it does not declare -
-- which OpenSCAD accepts silently, so the model would simply come out with
-- no name on it.
SELECT * FROM product_field_maps
WHERE variant_id = sqlc.arg('variant_id')
  AND role = sqlc.arg('role')
ORDER BY position, lower(scad_variable);

-- name: SetVariantPersonalisationLink :one
-- Turns one SKU's personalisation link on or off.
--
-- Off means the customer is not asked and the thirty-minute hold does not
-- happen, so the job renders and batches straight away - which is what a
-- product nobody needs to ask about should do.
UPDATE product_variants
SET personalisation_link_enabled = sqlc.arg('enabled'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListTemplateUsage :many
-- What every design file is used FOR, across the whole registry.
--
-- The Designs tab is a library of .scad files, and a library that does not say
-- what each book is for is a shelf. One template usually prints several
-- variants - swapping dnp_two_heart changes every two-heart variant at once -
-- so the useful unit is the PRODUCT and the ROLE, not the variant: "PWDH
-- prints from this, and draws its customer preview from it too".
--
-- A FILE CAN HOLD TWO ROLES AT ONCE, and this is why the role is carried
-- rather than collapsed. lotus_diya_holder.scad draws both the plate that
-- prints and the assembled picture the customer approves, so PWDH links it
-- twice; a query that returned one row per (key, product) would have to pick
-- one of those and would be wrong about the other.
--
-- Active designs only, and the SKU count with them: a template linked by three
-- colourways of one product is one row saying three, not three rows.
SELECT lower(d.template_key)::text AS template_key,
       p.code                      AS product_code,
       p.name                      AS product_name,
       d.role                      AS role,
       count(*)                    AS sku_count
FROM variant_designs d
JOIN product_variants v ON v.id = d.variant_id
JOIN products p ON p.id = v.product_id
WHERE d.status = 'active' AND d.template_key IS NOT NULL
GROUP BY lower(d.template_key), p.code, p.name, d.role
ORDER BY p.code, d.role;

-- name: ListAllComponentPlates :many
-- Every stored part file in the registry, with the part it belongs to.
--
-- The Designs tab's third section. These are NOT design templates - they are
-- finished files kept against a component, and nothing in Tensor opens them -
-- but they are files somebody uploaded into the registry, and a page that
-- claims to show what files exist and silently omits a whole kind of them is
-- worse than one that never claimed it.
--
-- The component's name comes along because the file alone says nothing: a
-- G-code called plate.gcode is every G-code anybody ever exported.
SELECT p.id,
       p.sku,
       p.machine_family,
       p.notes,
       p.uploaded_by,
       p.created_at,
       i.id   AS item_id,
       i.name AS item_name,
       i.code AS item_code,
       f.filename,
       f.size_bytes
FROM component_plates p
JOIN inventory_items i ON i.id = p.inventory_item_id
JOIN file_assets f ON f.id = p.file_id
ORDER BY lower(i.name), lower(p.sku), upper(p.machine_family);
