-- +goose Up
-- Print files built in the background for the Product Creation page.
--
-- The sized moon lamp's top and cap are cut by OpenSCAD on the production
-- worker (the API has no renderer), so asking for one is a record here and a
-- job on the queue; the page polls the record and downloads the finished
-- file from object storage. A finished build is reused when the same file is
-- asked for again, so a second click is instant.
CREATE TABLE IF NOT EXISTS product_creation_builds (
    id           uuid PRIMARY KEY,
    brand_slug   varchar(64)  NOT NULL,
    order_number varchar(64)  NOT NULL,
    product      varchar(32)  NOT NULL,
    variant      varchar(32)  NOT NULL DEFAULT '',
    status       varchar(16)  NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'building', 'done', 'failed')),
    storage_key  text,
    filename     varchar(200),
    error        text,
    requested_by varchar(64)  NOT NULL DEFAULT '',
    created_at   timestamptz  NOT NULL DEFAULT now(),
    updated_at   timestamptz  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_product_creation_builds_lookup
    ON product_creation_builds (brand_slug, product, lower(order_number), created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS product_creation_builds;
