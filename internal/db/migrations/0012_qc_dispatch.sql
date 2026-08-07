-- +goose Up
-- Phase 3 of the production pipeline: the append-only station records (assembly,
-- QC, packaging) and dispatch. Each record is written once as a job passes a
-- station; the job's *_status columns (set on production_jobs) are the current
-- state, these tables are the audit trail. Actor ids are Better Auth user ids
-- (varchar(64), no FK). Written idempotently.

-- One assembly check per submission (append-only).
CREATE TABLE IF NOT EXISTS production_job_assembly_checks (
    id                uuid PRIMARY KEY,
    job_id            uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    parts_combined    boolean NOT NULL DEFAULT false,
    hardware_attached boolean NOT NULL DEFAULT false,
    addons_attached   boolean NOT NULL DEFAULT false,
    fit_check_ok      boolean NOT NULL DEFAULT false,
    photo_file_id     uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    notes             varchar(1000),
    assembled_by      varchar(64) NOT NULL,
    assembled_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_assembly_checks_job_id ON production_job_assembly_checks (job_id);

-- One QC check per submission (append-only). The nine-point checklist is advisory;
-- the decision (pass|fail) is what drives qc_status and any reprint.
CREATE TABLE IF NOT EXISTS production_job_qc_checks (
    id                      uuid PRIMARY KEY,
    job_id                  uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    correct_personalisation boolean NOT NULL DEFAULT false,
    correct_colour          boolean NOT NULL DEFAULT false,
    surface_finish_ok       boolean NOT NULL DEFAULT false,
    no_cracks               boolean NOT NULL DEFAULT false,
    no_layer_defects        boolean NOT NULL DEFAULT false,
    dimensions_ok           boolean NOT NULL DEFAULT false,
    assembly_fit_ok         boolean NOT NULL DEFAULT false,
    addons_working          boolean NOT NULL DEFAULT false,
    packaging_safe          boolean NOT NULL DEFAULT false,
    photo_file_id           uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    decision                varchar(16) NOT NULL,
    notes                   varchar(1000),
    inspected_by            varchar(64) NOT NULL,
    inspected_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_qc_checks_job_id ON production_job_qc_checks (job_id);

-- Packaging details, one row per job (unique). Re-packaging updates the row.
CREATE TABLE IF NOT EXISTS production_job_packaging_details (
    id                uuid PRIMARY KEY,
    job_id            uuid NOT NULL UNIQUE REFERENCES production_jobs (id) ON DELETE CASCADE,
    packaging_type    varchar(128) NOT NULL,
    addons            varchar(500),
    gift_message      varchar(500),
    fragile           boolean NOT NULL DEFAULT false,
    courier_partner   varchar(128),
    invoice_reference varchar(128),
    photo_file_id     uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    packed_by         varchar(64) NOT NULL,
    packed_at         timestamptz NOT NULL DEFAULT now()
);

-- A dispatch record for an order: status pending -> dispatched.
CREATE TABLE IF NOT EXISTS dispatch_orders (
    id              uuid PRIMARY KEY,
    order_id        uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    carrier         varchar(100),
    tracking_number varchar(100),
    status          varchar(32) NOT NULL DEFAULT 'pending',
    dispatched_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_dispatch_orders_order_id ON dispatch_orders (order_id);
CREATE INDEX IF NOT EXISTS ix_dispatch_orders_created ON dispatch_orders (created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS dispatch_orders;
DROP TABLE IF EXISTS production_job_packaging_details;
DROP TABLE IF EXISTS production_job_qc_checks;
DROP TABLE IF EXISTS production_job_assembly_checks;
