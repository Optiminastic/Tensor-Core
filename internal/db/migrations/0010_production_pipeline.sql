-- +goose Up
-- The production pipeline (ported from print-queue-be): Shopify orders become
-- production jobs that move through a print -> assembly -> QC -> packaging
-- lifecycle, are grouped onto machine beds as batches, and finally dispatched.
-- Phase 1 lays the foundation: orders (read), file assets, and the production
-- job with its full lifecycle state. Batches, dispatch and the QC/assembly/
-- packaging audit tables arrive in later migrations; the forward-referencing
-- columns here (batch_id, shop_connection_id) are FK-less until then.
--
-- Adapted to Tensor-Core: actor ids are Better Auth user ids (varchar(64), no
-- users table, no FK - like designs.created_by); model files live in object
-- storage keyed by storage_key (MinIO), not Cloudinary URLs. Written idempotently.

-- A stored file (uploaded model, QC/packaging photo, or a generated merged plate).
-- bbox_* is the model's axis-aligned bounding box in mm, null for non-model files.
CREATE TABLE IF NOT EXISTS file_assets (
    id           uuid PRIMARY KEY,
    filename     varchar(255) NOT NULL,
    content_type varchar(127) NOT NULL,
    size_bytes   bigint NOT NULL,
    storage_key  text NOT NULL,
    is_template  boolean NOT NULL DEFAULT false,
    uploaded_by  varchar(64) NOT NULL,
    bbox_x_mm    numeric(10, 2),
    bbox_y_mm    numeric(10, 2),
    bbox_z_mm    numeric(10, 2),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_file_assets_uploaded_by ON file_assets (uploaded_by);

-- An order imported from Shopify. shop_connection_id gains its FK in the Shopify
-- phase; today orders may be seeded for testing. line_items is the raw per-line
-- snapshot the jobs are decomposed from.
CREATE TABLE IF NOT EXISTS orders (
    id                 uuid PRIMARY KEY,
    shop_connection_id uuid,
    shopify_order_id   bigint NOT NULL,
    order_number       varchar(64) NOT NULL,
    customer_name      varchar(255),
    financial_status   varchar(32) NOT NULL,
    total_price        numeric(10, 2) NOT NULL,
    currency           varchar(3) NOT NULL,
    line_items         jsonb NOT NULL DEFAULT '[]',
    status             varchar(32) NOT NULL DEFAULT 'queued',
    imported_at        timestamptz NOT NULL DEFAULT now(),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS ix_orders_shopify_order_id ON orders (shopify_order_id);
CREATE INDEX IF NOT EXISTS ix_orders_imported ON orders (imported_at DESC, id DESC);

-- A production job: one Shopify line item to print, with a snapshot of everything
-- needed to print, batch, personalise, QC and pack it. status is the primary
-- lifecycle (queued -> in_production -> completed | failed); assembly/qc/packaging/
-- personalisation are parallel sub-states. batch_id gets its FK with the batches
-- table; the three file references point at file_assets.
CREATE TABLE IF NOT EXISTS production_jobs (
    id                            uuid PRIMARY KEY,
    job_number                    varchar(64) NOT NULL,
    order_id                      uuid REFERENCES orders (id) ON DELETE SET NULL,
    batch_id                      uuid,
    description                   varchar(255) NOT NULL,
    quantity                      integer NOT NULL DEFAULT 1,
    status                        varchar(32) NOT NULL DEFAULT 'queued',
    assembly_status               varchar(32) NOT NULL DEFAULT 'pending',
    qc_status                     varchar(32) NOT NULL DEFAULT 'pending',
    packaging_status              varchar(32) NOT NULL DEFAULT 'pending',
    shopify_order_id              bigint,
    sku                           varchar(128),
    product_name                  varchar(255),
    material                      varchar(255),
    colour                        varchar(255),
    nozzle_profile                varchar(255),
    filament_grams_required       numeric(10, 2),
    print_file_id                 uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    estimated_print_time_minutes  integer,
    due_date                      timestamptz,
    priority                      integer NOT NULL DEFAULT 0,
    personalisation_name          varchar(255),
    personalisation_font          varchar(255),
    personalisation_colour        varchar(255),
    personalisation_variant       varchar(255),
    personalisation_status        varchar(32) NOT NULL DEFAULT 'pending',
    name_confirmed                boolean NOT NULL DEFAULT false,
    photo_confirmed               boolean NOT NULL DEFAULT false,
    font_confirmed                boolean NOT NULL DEFAULT false,
    colour_confirmed              boolean NOT NULL DEFAULT false,
    variant_confirmed             boolean NOT NULL DEFAULT false,
    customer_approval_received    boolean NOT NULL DEFAULT false,
    personalisation_notes         varchar(1000),
    personalisation_photo_file_id uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    personalisation_validated_by  varchar(64),
    personalisation_validated_at  timestamptz,
    reprint_of_job_id             uuid REFERENCES production_jobs (id) ON DELETE SET NULL,
    held                          boolean NOT NULL DEFAULT false,
    created_at                    timestamptz NOT NULL DEFAULT now(),
    updated_at                    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_production_jobs_order_id ON production_jobs (order_id);
CREATE INDEX IF NOT EXISTS ix_production_jobs_batch_id ON production_jobs (batch_id);
CREATE INDEX IF NOT EXISTS ix_production_jobs_created ON production_jobs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS ix_production_jobs_personalisation ON production_jobs (personalisation_status);
CREATE INDEX IF NOT EXISTS ix_production_jobs_reprint_of ON production_jobs (reprint_of_job_id);
CREATE INDEX IF NOT EXISTS ix_production_jobs_shopify_order_id ON production_jobs (shopify_order_id);

-- Append-only record of a job failing at print (or later QC). Written by /fail;
-- the reason drives waste/cost reporting and the wasted grams feed the filament
-- decrement once inventory exists (later phase).
CREATE TABLE IF NOT EXISTS production_job_failures (
    id                    uuid PRIMARY KEY,
    job_id                uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    stage                 varchar(16) NOT NULL,
    reason                varchar(64) NOT NULL,
    notes                 varchar(1000),
    filament_wasted_grams numeric(10, 2),
    time_wasted_minutes   integer,
    created_by            varchar(64) NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_production_job_failures_job_id ON production_job_failures (job_id);

-- Machines are operational for the print queue: only 'online' machines are
-- batch-eligible. The cost side of machine_profiles is unchanged.
ALTER TABLE machine_profiles ADD COLUMN IF NOT EXISTS status varchar(32) NOT NULL DEFAULT 'offline';

-- +goose Down
ALTER TABLE machine_profiles DROP COLUMN IF EXISTS status;
DROP TABLE IF EXISTS production_job_failures;
DROP TABLE IF EXISTS production_jobs;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS file_assets;
