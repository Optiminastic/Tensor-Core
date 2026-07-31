-- Plain DDL used ONLY by sqlc to build its type catalog for code generation.
-- The runtime source of truth is the goose migrations in internal/db/migrations
-- (idempotent). These must describe the same final schema; the DB integration
-- tests apply the migrations and then run every sqlc query, so any drift fails.

-- Brands are now free-form (user-created), so `brand` is plain text, referenced
-- by a brand's slug -- there is no brand enum anymore. project_status stays a
-- fixed set, modelled as a domain (sqlc maps a domain to its base text type).
CREATE DOMAIN project_status AS text CHECK (VALUE IN ('active', 'archived'));

CREATE TABLE cost_assumption_sets (
    id                        uuid PRIMARY KEY,
    name                      varchar(120) NOT NULL UNIQUE,
    brand                     text,
    filament_cost_per_kg      numeric(10, 2) NOT NULL,
    electricity_cost_per_unit numeric(10, 2) NOT NULL,
    machine_hour_cost         numeric(10, 2) NOT NULL,
    finishing_labour          numeric(10, 2) NOT NULL,
    consumables               numeric(10, 2) NOT NULL,
    failure_pct               numeric(5, 4) NOT NULL,
    fixed_costs               json NOT NULL DEFAULT '{}',
    margins                   json NOT NULL DEFAULT '{}',
    is_default                boolean NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE machine_profiles (
    id                uuid PRIMARY KEY,
    name              varchar(120) NOT NULL UNIQUE,
    machine_hour_cost numeric(10, 2) NOT NULL,
    is_active         boolean NOT NULL,
    -- Operational status for the print queue: online | busy | offline | maintenance.
    -- Only 'online' machines are eligible to run a batch.
    status            varchar(32) NOT NULL DEFAULT 'offline',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE material_profiles (
    id            uuid PRIMARY KEY,
    name          varchar(120) NOT NULL UNIQUE,
    material_type varchar(40) NOT NULL,
    cost_per_kg   numeric(10, 2) NOT NULL,
    colour        varchar(60),
    is_active     boolean NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    id          uuid PRIMARY KEY,
    name        varchar(40) NOT NULL UNIQUE,
    description varchar(200) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE permissions (
    id          uuid PRIMARY KEY,
    resource    varchar(40) NOT NULL,
    action      varchar(40) NOT NULL,
    description varchar(200) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_permission_resource_action UNIQUE (resource, action)
);

CREATE TABLE role_permissions (
    role_id       uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permissions (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE user_roles (
    user_id     varchar(64) NOT NULL,
    role_id     uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    assigned_by varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX ix_user_roles_user_id ON user_roles (user_id);

CREATE TABLE user_authz_state (
    user_id             varchar(64) PRIMARY KEY,
    permissions_version integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_invites (
    id               uuid PRIMARY KEY,
    email            varchar(255) NOT NULL,
    role_id          uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    token_hash       varchar(64) NOT NULL UNIQUE,
    expires_at       timestamptz NOT NULL,
    accepted_at      timestamptz,
    accepted_user_id varchar(64),
    revoked_at       timestamptz,
    created_by       varchar(64),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_user_invites_email ON user_invites (email);
CREATE INDEX ix_user_invites_created ON user_invites (created_at DESC, id DESC);

CREATE TABLE brands (
    id                  uuid PRIMARY KEY,
    slug                text NOT NULL UNIQUE,
    name                varchar(120) NOT NULL,
    logo_url            text,
    starting_price      numeric(10, 2) NOT NULL,
    shopify_url         varchar(255),
    description         varchar(500),
    is_active           boolean NOT NULL,
    ladder              json NOT NULL,
    cp_green_max        numeric(5, 4) NOT NULL,
    cp_yellow_max       numeric(5, 4) NOT NULL,
    entry_machine_hours numeric(5, 2),
    entry_rung          integer,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id          uuid PRIMARY KEY,
    name        varchar(120) NOT NULL UNIQUE,
    brand       text NOT NULL REFERENCES brands (slug) ON DELETE RESTRICT,
    description varchar(500),
    status      project_status NOT NULL,
    created_by  varchar(64) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Per-brand connections to external ad and commerce platforms. Tokens are stored
-- so the platform can be called on the brand's behalf; one row per (brand,
-- provider). status is 'disconnected' | 'connected' | 'error'.
CREATE TABLE brand_connections (
    id                  uuid PRIMARY KEY,
    brand_slug          text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    provider            text NOT NULL,
    status              text NOT NULL,
    external_account_id text,
    access_token        text,
    refresh_token       text,
    expires_at          timestamptz,
    connected_by        varchar(64),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_brand_connection UNIQUE (brand_slug, provider)
);

-- The design pipeline (see migration 0003). A design is the uploaded model plus
-- the answers that drive slicing; status is queued -> slicing -> priced | failed.
CREATE TABLE designs (
    id            uuid PRIMARY KEY,
    brand_slug    text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    name          varchar(160) NOT NULL,
    created_by    varchar(64) NOT NULL,
    status        text NOT NULL,
    stl_key       text NOT NULL,
    material      varchar(20) NOT NULL,
    colour        varchar(60),
    finish        varchar(20) NOT NULL,
    units_per_bed integer NOT NULL,
    quality       varchar(20) NOT NULL,
    infill_pct    numeric(5, 2) NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_designs_brand_slug ON designs (brand_slug);
CREATE INDEX ix_designs_brand_created ON designs (brand_slug, created_at DESC, id DESC);

CREATE TABLE slice_jobs (
    id         uuid PRIMARY KEY,
    design_id  uuid NOT NULL REFERENCES designs (id) ON DELETE CASCADE,
    status     text NOT NULL,
    attempt    integer NOT NULL DEFAULT 1,
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_slice_jobs_design_id ON slice_jobs (design_id);
CREATE UNIQUE INDEX uq_slice_jobs_design_attempt ON slice_jobs (design_id, attempt);

CREATE TABLE slice_metrics (
    job_id                    uuid PRIMARY KEY REFERENCES slice_jobs (id) ON DELETE CASCADE,
    print_time_hr             numeric(10, 4) NOT NULL,
    effective_machine_time_hr numeric(10, 4) NOT NULL,
    filament_g                numeric(10, 3) NOT NULL,
    purge_g                   numeric(10, 3) NOT NULL DEFAULT 0,
    support_g                 numeric(10, 3) NOT NULL DEFAULT 0,
    colour_changes            integer NOT NULL DEFAULT 0,
    electricity_kwh           numeric(10, 4) NOT NULL DEFAULT 0,
    units_per_bed             integer NOT NULL,
    layer_height_mm           numeric(6, 3) NOT NULL DEFAULT 0,
    infill_density_pct        numeric(6, 2) NOT NULL DEFAULT 0,
    wall_loops                integer NOT NULL DEFAULT 0,
    support_used              boolean NOT NULL DEFAULT false,
    filament_length_mm        numeric(12, 2) NOT NULL DEFAULT 0,
    gcode_key                 text NOT NULL DEFAULT '',
    orientation               jsonb,
    created_at                timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE design_pricing (
    design_id             uuid PRIMARY KEY REFERENCES designs (id) ON DELETE CASCADE,
    design_cp             numeric(12, 2) NOT NULL,
    breakdown             json NOT NULL,
    verdict               text NOT NULL,
    cp_pct                numeric(6, 4) NOT NULL,
    recommended_sp        integer,
    raw_sp                numeric(12, 2) NOT NULL DEFAULT 0,
    cp_pct_at_recommended numeric(6, 4),
    passes_normal         boolean NOT NULL DEFAULT false,
    survives_stress       boolean NOT NULL DEFAULT false,
    sp_warnings           json NOT NULL DEFAULT '[]',
    reasons               json NOT NULL,
    suggestions           json NOT NULL,
    approved_sp           integer,
    approved_by           varchar(64),
    approved_at           timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE shopify_products (
    design_id    uuid PRIMARY KEY REFERENCES designs (id) ON DELETE CASCADE,
    brand_slug   text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    product_gid  text NOT NULL,
    variant_gid  text NOT NULL DEFAULT '',
    handle       text NOT NULL DEFAULT '',
    admin_url    text NOT NULL DEFAULT '',
    status       text NOT NULL,
    published_by varchar(64),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- The production pipeline (migration 0010). Actor ids are Better Auth user ids
-- (varchar(64), no FK). Model files live in object storage keyed by storage_key.

CREATE TABLE file_assets (
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
CREATE INDEX ix_file_assets_uploaded_by ON file_assets (uploaded_by);

CREATE TABLE orders (
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
CREATE UNIQUE INDEX ix_orders_shopify_order_id ON orders (shopify_order_id);
CREATE INDEX ix_orders_imported ON orders (imported_at DESC, id DESC);

CREATE TABLE production_jobs (
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
CREATE INDEX ix_production_jobs_order_id ON production_jobs (order_id);
CREATE INDEX ix_production_jobs_batch_id ON production_jobs (batch_id);
CREATE INDEX ix_production_jobs_created ON production_jobs (created_at DESC, id DESC);
CREATE INDEX ix_production_jobs_personalisation ON production_jobs (personalisation_status);
CREATE INDEX ix_production_jobs_reprint_of ON production_jobs (reprint_of_job_id);
CREATE INDEX ix_production_jobs_shopify_order_id ON production_jobs (shopify_order_id);

CREATE TABLE production_job_failures (
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
CREATE INDEX ix_production_job_failures_job_id ON production_job_failures (job_id);

CREATE TABLE batches (
    id                              uuid PRIMARY KEY,
    batch_number                    varchar(64) NOT NULL,
    machine_id                      uuid REFERENCES machine_profiles (id) ON DELETE SET NULL,
    status                          varchar(32) NOT NULL DEFAULT 'open',
    approved_by                     varchar(64),
    approved_at                     timestamptz,
    material_shortage               boolean NOT NULL DEFAULT false,
    merged_file_id                  uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    preview_file_id                 uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    units_per_bed                   integer,
    total_print_time_minutes        integer,
    effective_time_per_unit_minutes numeric(10, 2),
    total_filament_grams            numeric(10, 2),
    bed_utilization_percent         numeric(5, 2),
    packing_strategy                varchar(32),
    created_at                      timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_batches_created ON batches (created_at DESC, id DESC);
CREATE INDEX ix_batches_machine_status ON batches (machine_id, status);

ALTER TABLE production_jobs
    ADD CONSTRAINT fk_production_jobs_batch_id
    FOREIGN KEY (batch_id) REFERENCES batches (id) ON DELETE SET NULL;

CREATE TABLE filament_inventory (
    id                  uuid PRIMARY KEY,
    material            varchar(255) NOT NULL,
    colour              varchar(255),
    grams_available     numeric(10, 2) NOT NULL DEFAULT 0,
    reorder_level_grams numeric(10, 2) NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_filament_material_colour
    ON filament_inventory (material, COALESCE(colour, ''));

CREATE TABLE production_job_assembly_checks (
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
CREATE INDEX ix_assembly_checks_job_id ON production_job_assembly_checks (job_id);

CREATE TABLE production_job_qc_checks (
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
CREATE INDEX ix_qc_checks_job_id ON production_job_qc_checks (job_id);

CREATE TABLE production_job_packaging_details (
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

CREATE TABLE dispatch_orders (
    id              uuid PRIMARY KEY,
    order_id        uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    carrier         varchar(100),
    tracking_number varchar(100),
    status          varchar(32) NOT NULL DEFAULT 'pending',
    dispatched_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_dispatch_orders_order_id ON dispatch_orders (order_id);
CREATE INDEX ix_dispatch_orders_created ON dispatch_orders (created_at DESC, id DESC);

CREATE TABLE shopify_connections (
    id                      uuid PRIMARY KEY,
    shop_domain             varchar(255) NOT NULL,
    encrypted_access_token  text NOT NULL,
    scopes                  text,
    webhook_subscription_id varchar(255),
    is_active               boolean NOT NULL DEFAULT true,
    connected_at            timestamptz NOT NULL DEFAULT now(),
    disconnected_at         timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ix_shopify_connections_active_domain
    ON shopify_connections (shop_domain) WHERE is_active;

ALTER TABLE orders
    ADD CONSTRAINT fk_orders_shop_connection_id
    FOREIGN KEY (shop_connection_id) REFERENCES shopify_connections (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX uq_orders_shop_order_number
    ON orders (shop_connection_id, order_number);
