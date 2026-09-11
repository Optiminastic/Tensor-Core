-- +goose Up
-- What a product IS, as against what happened to one.
--
-- Everything else in this schema is transactional - an order arrived, a job was
-- made, a bed printed. This is master data: which products are sold, in what
-- variants, printed from which file, built from which parts. It exists because
-- that knowledge currently lives in Go constants and three embedded .scad
-- files, so adding a colour or a heart count needs a developer, a recompile and
-- a deploy.
--
-- The shape is Product -> Option -> Variant -> (Design, BOM). A variant is a
-- COMBINATION of option values, never a product of its own: the storefront
-- already sells 41 distinct product names for what is really one plank times
-- colour times light option, and modelling that as 41 products is how the next
-- colour becomes 82.

CREATE TABLE IF NOT EXISTS products (
    id     uuid PRIMARY KEY,
    -- The family code an order's SKU carries: DNP, DNPF, DNPLB.
    code   varchar(32)  NOT NULL,
    name   varchar(160) NOT NULL,
    -- 'generated' - Tensor renders it from a template, and needs no design
    -- file uploaded (the Dual Name Plank). 'uploaded' - somebody must supply a
    -- 3MF before it can print (the photo frame). This distinction already
    -- drives IsGeneratedProduct and the whole "Design file required" path; the
    -- registry should own it instead of a hardcoded map.
    kind   varchar(16)  NOT NULL DEFAULT 'generated',
    status varchar(16)  NOT NULL DEFAULT 'active',
    notes  text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_products_code ON products (lower(code));

-- One axis of choice: heart_count, light, colour.
CREATE TABLE IF NOT EXISTS product_options (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    code       varchar(32)  NOT NULL,
    label      varchar(80)  NOT NULL,
    -- Display order, so "0 · 1 · 2" is not shown as "1 · 0 · 2".
    position   integer      NOT NULL DEFAULT 0,
    created_at timestamptz  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_option_code
    ON product_options (product_id, lower(code));

CREATE TABLE IF NOT EXISTS product_option_values (
    id        uuid PRIMARY KEY,
    option_id uuid NOT NULL REFERENCES product_options (id) ON DELETE CASCADE,
    code      varchar(48) NOT NULL,
    label     varchar(80) NOT NULL,
    position  integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_option_value_code
    ON product_option_values (option_id, lower(code));

-- One sellable combination.
CREATE TABLE IF NOT EXISTS product_variants (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- Nullable, and deliberately so. Nine live plank lines carry no SKU at all
    -- and are matched by product name today; a registry that assumed Shopify
    -- was tidy would have nowhere to put them.
    sku        varchar(128),
    name       varchar(200) NOT NULL,
    status     varchar(16)  NOT NULL DEFAULT 'active',
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_variant_sku
    ON product_variants (lower(sku)) WHERE sku IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_product_variants_product ON product_variants (product_id);

-- Which option values this variant is. The join that makes a variant a
-- combination rather than a row of its own columns.
CREATE TABLE IF NOT EXISTS product_variant_options (
    variant_id      uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    option_value_id uuid NOT NULL REFERENCES product_option_values (id) ON DELETE CASCADE,
    PRIMARY KEY (variant_id, option_value_id)
);

-- Which file prints this variant.
--
-- template_key names an embedded .scad for a generated product
-- ("dnp_two_heart"); design_id points at the designs table for an uploaded one.
-- Exactly one is set, which the CHECK enforces rather than trusting a caller.
CREATE TABLE IF NOT EXISTS variant_designs (
    id           uuid PRIMARY KEY,
    variant_id   uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    -- 'body' is the product itself; 'base' is the light box it sits on. A
    -- with-light variant needs both, and they are separate prints.
    role         varchar(24) NOT NULL DEFAULT 'body',
    template_key varchar(64),
    design_id    uuid REFERENCES designs (id) ON DELETE SET NULL,
    version      integer     NOT NULL DEFAULT 1,
    status       varchar(16) NOT NULL DEFAULT 'active',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_variant_design_target CHECK (
        (template_key IS NOT NULL AND design_id IS NULL)
        OR (template_key IS NULL AND design_id IS NOT NULL)
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_variant_design_role
    ON variant_designs (variant_id, role) WHERE status = 'active';

-- The parts, pointing at the shelf that already exists.
CREATE TABLE IF NOT EXISTS variant_bom (
    id                uuid PRIMARY KEY,
    variant_id        uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    -- RESTRICT, not CASCADE: deleting a part that products are built from must
    -- fail loudly rather than silently emptying their bills of materials.
    inventory_item_id uuid NOT NULL REFERENCES inventory_items (id) ON DELETE RESTRICT,
    quantity          numeric(12, 3) NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_variant_bom_item
    ON variant_bom (variant_id, inventory_item_id);

-- +goose Down
DROP TABLE IF EXISTS variant_bom;
DROP TABLE IF EXISTS variant_designs;
DROP TABLE IF EXISTS product_variant_options;
DROP TABLE IF EXISTS product_variants;
DROP TABLE IF EXISTS product_option_values;
DROP TABLE IF EXISTS product_options;
DROP TABLE IF EXISTS products;
