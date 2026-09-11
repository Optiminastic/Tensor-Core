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
SELECT d.variant_id, d.role, d.template_key, d.design_id, d.version
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

-- name: GetProductByID :one
SELECT * FROM products WHERE id = sqlc.arg('id');

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

-- name: GetProductOption :one
SELECT * FROM product_options WHERE id = sqlc.arg('id');

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
