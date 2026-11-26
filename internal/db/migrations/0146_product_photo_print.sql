-- +goose Up
-- The size a product's customer photo prints at, for the Custom Photo sheets.
--
-- OPTIONAL: most products carry no photo, and one with no rows here is simply
-- not a photo product (or falls back to the sizes Tensor ships for DuoSnap,
-- fridge magnets, Family Crossword and social stickers).
--
-- A product can have several sizes, chosen by an order field: the Family
-- Crossword's photo is 106 x 80 mm with 3 frames and 71 x 54 mm with 6. A row
-- whose when_field is empty is the default. Sizes are millimetres; the page
-- takes centimetres too and converts.
CREATE TABLE IF NOT EXISTS product_photo_sizes (
    id          uuid PRIMARY KEY,
    product_id  uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    width_mm    double precision NOT NULL,
    height_mm   double precision NOT NULL,
    -- 'rect', or 'circle' (width is the diameter).
    shape       varchar(8) NOT NULL DEFAULT 'rect',
    when_field  varchar(64) NOT NULL DEFAULT '',
    when_value  varchar(128) NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    updated_by  varchar(255) NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT product_photo_sizes_positive CHECK (width_mm > 0 AND height_mm > 0),
    CONSTRAINT product_photo_sizes_shape CHECK (shape IN ('rect', 'circle'))
);
CREATE INDEX IF NOT EXISTS ix_product_photo_sizes_product ON product_photo_sizes (product_id, position);

-- The sample PDF showing the photo at its exact size on A4: reference for
-- whoever sets the sizes and prints the sheets. One per product.
CREATE TABLE IF NOT EXISTS product_photo_samples (
    product_id   uuid PRIMARY KEY REFERENCES products (id) ON DELETE CASCADE,
    filename     varchar(255) NOT NULL,
    storage_key  varchar(512) NOT NULL,
    size_bytes   bigint NOT NULL,
    uploaded_by  varchar(255) NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS product_photo_samples;
DROP TABLE IF EXISTS product_photo_sizes;
