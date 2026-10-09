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
    id                   uuid PRIMARY KEY,
    name                 varchar(120) NOT NULL UNIQUE,
    machine_hour_cost    numeric(10, 2) NOT NULL,
    is_active            boolean NOT NULL,
    -- Operational status for the print queue: online | busy | offline | maintenance.
    -- Only 'online' machines are eligible to run a batch.
    status               varchar(32) NOT NULL DEFAULT 'offline',
    -- Slicing config, exposed via /machines (distinct from /config/machines'
    -- cost-only view). right_nozzle_mm is null for single-nozzle machines.
    family               varchar(16) NOT NULL DEFAULT 'H2S',
    nozzle_mm            numeric(3, 2) NOT NULL DEFAULT 0.4,
    right_nozzle_mm      numeric(3, 2),
    flow                 varchar(16) NOT NULL DEFAULT 'standard',
    -- The right nozzle's flow setting (dual-nozzle machines only); null for a
    -- single-nozzle profile, matching right_nozzle_mm's nullability.
    right_flow           varchar(16),
    default_colour       varchar(40),
    layer_height_min_mm  numeric(4, 2) NOT NULL DEFAULT 0.08,
    layer_height_max_mm  numeric(4, 2) NOT NULL DEFAULT 0.28,
    supported_filaments  jsonb NOT NULL DEFAULT '[]',
    is_default           boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_machine_profiles_default ON machine_profiles (is_default) WHERE is_default;

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
    sku           varchar(64),
    -- Which printer profile this design was sliced for; the job-creation
    -- worker snapshots its nozzle/quality facts onto a matched job.
    machine_id    uuid REFERENCES machine_profiles (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_designs_brand_slug ON designs (brand_slug);
CREATE INDEX ix_designs_brand_created ON designs (brand_slug, created_at DESC, id DESC);
CREATE UNIQUE INDEX uq_designs_sku ON designs (sku) WHERE sku IS NOT NULL;

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
    -- What a generated model was built from: template, heart count and both
    -- names as given to OpenSCAD. Null on an uploaded file, and on anything
    -- rendered before 0073 - see that migration for why the filename could not
    -- answer this.
    render_params jsonb,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_file_assets_uploaded_by ON file_assets (uploaded_by);

CREATE TABLE orders (
    id                  uuid PRIMARY KEY,
    shop_connection_id  uuid,
    shopify_order_id    bigint NOT NULL,
    order_number        varchar(64) NOT NULL,
    customer_name       varchar(255),
    shopify_customer_id bigint,
    customer_email      varchar(255),
    customer_phone      varchar(64),
    financial_status    varchar(32) NOT NULL,
    total_price         numeric(10, 2) NOT NULL,
    currency            varchar(3) NOT NULL,
    line_items          jsonb NOT NULL DEFAULT '[]',
    status              varchar(32) NOT NULL DEFAULT 'queued',
    source              varchar(20) NOT NULL DEFAULT 'shopify_webhook'
        CHECK (source IN ('shopify_webhook', 'seed')),
    imported_at         timestamptz NOT NULL DEFAULT now(),
    -- Why the create_jobs_from_order River job gave up, and when. Null on a
    -- healthy order; see migration 0034.
    job_creation_error     text,
    job_creation_failed_at timestamptz,
    -- Shopify's own order page, mirrored - see migration 0061. Money is
    -- nullable because an order imported before that migration does not know
    -- these figures, and a zero would render as a confident wrong number.
    placed_at           timestamptz,
    note                text,
    attributes          jsonb NOT NULL DEFAULT '[]',
    tags                jsonb NOT NULL DEFAULT '[]',
    fulfillment_status  varchar(32),
    -- The carrier's view and the return state - see migration 0063.
    delivery_status     varchar(32),
    return_status       varchar(32),
    source_name         varchar(64),
    subtotal_price      numeric(10, 2),
    total_discounts     numeric(10, 2),
    total_shipping      numeric(10, 2),
    total_received      numeric(10, 2),
    discount_title      text,
    shipping_title      text,
    shipping_address    jsonb,
    billing_address     jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ix_orders_shopify_order_id ON orders (shopify_order_id);
CREATE INDEX ix_orders_job_creation_failed ON orders (job_creation_failed_at DESC)
    WHERE job_creation_error IS NOT NULL;
CREATE INDEX ix_orders_imported ON orders (imported_at DESC, id DESC);
CREATE INDEX ix_orders_source ON orders (source);

CREATE TABLE order_line_items (
    id                  uuid PRIMARY KEY,
    order_id            uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    shopify_order_id    bigint NOT NULL,
    shopify_customer_id bigint,
    sku                 varchar(120),
    product_name        varchar(255) NOT NULL,
    product_image_url   text,
    quantity            integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_order_line_items_order ON order_line_items (order_id);
CREATE INDEX ix_order_line_items_shopify_order ON order_line_items (shopify_order_id);

CREATE TABLE production_jobs (
    id                            uuid PRIMARY KEY,
    job_number                    varchar(64) NOT NULL,
    order_id                      uuid REFERENCES orders (id) ON DELETE SET NULL,
    batch_id                      uuid,
    description                   varchar(255) NOT NULL,
    quantity                      integer NOT NULL DEFAULT 1,
    status                        varchar(32) NOT NULL DEFAULT 'queued',
    assembly_status               varchar(32) NOT NULL DEFAULT 'pending',
    finishing_status              varchar(32) NOT NULL DEFAULT 'pending',
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
    -- Set when this row is a fragment peeled off another job's quantity
    -- because the whole amount didn't fit on one bed (see 0028_job_split.sql).
    split_of_job_id               uuid REFERENCES production_jobs (id) ON DELETE SET NULL,
    -- Denormalised from orders.shopify_customer_id / customer_name at job
    -- creation time (see buildJobsForOrder) - avoids a join for the planner
    -- and batch listings; both null when the order has no customer object
    -- (guest checkout) or the job has no linked order (see 0029).
    shopify_customer_id           bigint,
    customer_name                 varchar(255),
    held                          boolean NOT NULL DEFAULT false,
    -- Multi-colour set (e.g. ["Red","Yellow","Black"]) and the grouping/
    -- validation-stage snapshots: support usage and infill from the matched
    -- design's slice, dual-nozzle + quality from its printer profile, and the
    -- fixed issue-reason taxonomy from job creation (null = no issue).
    colours                       jsonb NOT NULL DEFAULT '[]',
    support_used                  boolean,
    infill_pct                    numeric(5, 2),
    left_nozzle_mm                numeric(3, 2),
    right_nozzle_mm               numeric(3, 2),
    flow_pct                      numeric(6, 2),
    quality_mm                    numeric(4, 3),
    machine_family                varchar(16),
    -- What the customer asked for, snapshotted at creation - see 0064.
    variant_title                 varchar(255),
    personalisation_properties    jsonb NOT NULL DEFAULT '[]',
    -- Which of its product's design files this job prints - see 0081. A
    -- product may print from several, and a combo prints three: a plank, a
    -- rose and a keychain, one job each.
    part_role                     varchar(24) NOT NULL DEFAULT 'body',
    -- Why a generated model could not be built - see migration 0065.
    model_error                   text,
    model_error_at                timestamptz,
    issue_reason                  varchar(32),
    -- Geometry/slice snapshot from the matched design at creation time (see
    -- 0030_job_geometry_snapshot.sql): bounding box from file_assets, and
    -- support/purge weight (already scaled to this job's quantity, same
    -- convention as filament_grams_required) plus colour count (per-unit,
    -- not scaled) from the design's latest slice metrics.
    bbox_x_mm                     numeric(10, 2),
    bbox_y_mm                     numeric(10, 2),
    bbox_z_mm                     numeric(10, 2),
    support_weight_g              numeric(10, 3),
    purge_weight_g                numeric(10, 3),
    colour_count                  integer,
    created_at                    timestamptz NOT NULL DEFAULT now(),
    updated_at                    timestamptz NOT NULL DEFAULT now(),
    -- The quotation this job was approved from, when it came from one. Null for
    -- every Shopify job. LAST, because migration 0089 adds it with ALTER TABLE
    -- ADD COLUMN, which appends - and this file has to describe the same table,
    -- column order included, or sqlc stops mapping SELECT * to gen.ProductionJob.
    bulk_order_id                 uuid REFERENCES bulk_orders (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX uq_production_jobs_job_number ON production_jobs (job_number);
CREATE INDEX ix_production_jobs_order_id ON production_jobs (order_id);
CREATE INDEX ix_production_jobs_batch_id ON production_jobs (batch_id);
CREATE INDEX ix_production_jobs_created ON production_jobs (created_at DESC, id DESC);
CREATE INDEX ix_production_jobs_personalisation ON production_jobs (personalisation_status);
CREATE INDEX ix_production_jobs_reprint_of ON production_jobs (reprint_of_job_id);
CREATE INDEX ix_production_jobs_split_of ON production_jobs (split_of_job_id);
CREATE INDEX ix_production_jobs_shopify_customer_id ON production_jobs (shopify_customer_id);
CREATE INDEX ix_production_jobs_shopify_order_id ON production_jobs (shopify_order_id);
CREATE INDEX ix_production_jobs_colours ON production_jobs USING gin (colours);
CREATE INDEX ix_production_jobs_issue ON production_jobs (issue_reason) WHERE issue_reason IS NOT NULL;

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
    -- The printer class this bed was laid out for - see migration 0083. The
    -- plate's offsets are fixed and the slicer is told not to rearrange them,
    -- so this is the only class that can print it.
    machine_family                  varchar(16),
    total_print_time_minutes        integer,
    effective_time_per_unit_minutes numeric(10, 2),
    total_filament_grams            numeric(10, 2),
    bed_utilization_percent         numeric(5, 2),
    packing_strategy                varchar(32),
    -- Set true the moment approveBatch debits filament stock, so a lost race
    -- against the pending_approval-only guard can't double-reserve.
    filament_reserved               boolean NOT NULL DEFAULT false,
    -- Built by a person, so the planner leaves it alone. It dissolves and
    -- rebuilds every Draft it proposed; this marks the ones it did not. See
    -- migration 0078.
    manual                          boolean NOT NULL DEFAULT false,
    -- Set when the merged plate itself was sliced (see 0036). While NULL,
    -- total_print_time_minutes is batchTimeFromJobs' MAX-of-jobs
    -- approximation rather than a measurement of this actual bed.
    plate_sliced_at                 timestamptz,
    plate_slice_error               text,
    -- Why the batch never reached the printer, and when. Null on a batch that
    -- either printed or was never sent (see 0044).
    print_error                     text,
    print_error_at                  timestamptz,
    -- BambuBuddy's queue item id, so the batch can be followed after sending.
    queue_item_id                   integer,
    -- What slicing the merged plate measured beyond time and total filament
    -- (see 0039). Plate-level figures, not sums of per-job estimates.
    total_layers                    integer,
    support_grams                   numeric(10, 2),
    purge_grams                     numeric(10, 2),
    colour_changes                  integer,
    filament_by_colour              jsonb NOT NULL DEFAULT '[]',
    created_at                      timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now(),
    -- The BambuBuddy slicer-pipeline run this batch was dispatched as. Set
    -- when the run is accepted (202), before any queue entry exists - see
    -- migration 0066.
    pipeline_run_id                 integer,
    -- The slice BambuBuddy is running for this bed right now, before any
    -- queue item exists. See migration 0075: without it a bed mid-slice
    -- looks undispatched and can be sent - and printed - twice.
    bambu_slice_job_id              integer,
    -- Which PHYSICAL printer this bed was sent to. batches.machine_id is a
    -- machine_profiles id - a class shared by up to five units - so it cannot
    -- answer this. See migration 0077.
    fleet_machine_id                uuid REFERENCES machines (id) ON DELETE SET NULL,
    -- What actually happened on the printer, from BambuBuddy's archive - see
    -- migration 0068. print_outcome is null until a print resolves and is the
    -- idempotency key for the write that resolves it. The actual_* pair sits
    -- beside the planned figures above rather than overwriting them.
    archive_id                      integer,
    print_outcome                   varchar(16),
    print_started_at                timestamptz,
    print_finished_at               timestamptz,
    actual_print_time_minutes       integer,
    actual_filament_grams           numeric(10, 2)
);
CREATE UNIQUE INDEX uq_batches_batch_number ON batches (batch_number);
CREATE INDEX ix_batches_queue_item ON batches (queue_item_id) WHERE queue_item_id IS NOT NULL;
CREATE INDEX ix_batches_fleet_machine ON batches (fleet_machine_id) WHERE fleet_machine_id IS NOT NULL;
CREATE INDEX ix_batches_archive ON batches (archive_id) WHERE archive_id IS NOT NULL;
CREATE INDEX ix_batches_unsliced ON batches (created_at DESC) WHERE plate_sliced_at IS NULL;
CREATE INDEX ix_batches_created ON batches (created_at DESC, id DESC);
CREATE INDEX ix_batches_machine_status ON batches (machine_id, status);

ALTER TABLE production_jobs
    ADD CONSTRAINT fk_production_jobs_batch_id
    FOREIGN KEY (batch_id) REFERENCES batches (id) ON DELETE SET NULL;

CREATE TABLE filament_inventory (
    id                  uuid PRIMARY KEY,
    material            varchar(255) NOT NULL,
    colour              varchar(255),
    -- The swatch for `colour`, e.g. '#1A1A1A'. A name is what the planner keys
    -- on; the hex is what an operator recognises on a shelf. Null when the row
    -- was entered by hand rather than synced (see 0043).
    colour_hex          varchar(9),
    grams_available     numeric(10, 2) NOT NULL DEFAULT 0,
    reorder_level_grams numeric(10, 2) NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_filament_material_colour
    ON filament_inventory (material, COALESCE(colour, ''));

-- What a colour NAME means to the printers that have to print it. See migration
-- 0074: an order says "BLUE", an AMS reports only a hex, and nothing else holds
-- both. A name accepts many hexes (thirteen printers do not agree on blue) with
-- exactly one primary - the swatch used for rendering and for the dialog.
--
-- Separate from filament_inventory.colour_hex on purpose: the spool sync
-- overwrites that column and deletes its row when the shelf stops reporting the
-- colour, either of which would silently discard an operator's confirmation.
CREATE TABLE colour_map (
    id           uuid PRIMARY KEY,
    colour_name  varchar(64) NOT NULL,
    hex          varchar(7) NOT NULL,
    is_primary   boolean NOT NULL DEFAULT false,
    note         text,
    confirmed_by varchar(255),
    confirmed_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_colour_map_pair
    ON colour_map (lower(trim(colour_name)), upper(hex));
CREATE UNIQUE INDEX uq_colour_map_primary
    ON colour_map (lower(trim(colour_name))) WHERE is_primary;

-- Everything on the shelf that is not filament: boxes, inserts, cards, tape.
-- Separate from filament_inventory because that table is keyed
-- (material, colour) and its grams are read by the planner, the colour resolver
-- and the reservation path - see migration 0067.
CREATE TABLE inventory_items (
    id         uuid PRIMARY KEY,
    name       varchar(255) NOT NULL,
    quantity   numeric(12, 3) NOT NULL DEFAULT 0,
    unit       varchar(32) NOT NULL,
    unit_price numeric(12, 2),
    -- A stable handle a bill of materials points at, so a BOM line survives the
    -- shelf being renamed - see migration 0069. Nullable: a code is only needed
    -- once a part is actually used by a product.
    code       varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_inventory_item_name
    ON inventory_items (lower(name));
CREATE UNIQUE INDEX uq_inventory_item_code
    ON inventory_items (lower(code)) WHERE code IS NOT NULL;

-- The physical printer fleet - one row per physical unit, live print-state.
-- Distinct from machine_profiles (the printer model/slicing profile).
CREATE TABLE machines (
    id                        uuid PRIMARY KEY,
    machine_id                varchar(64) NOT NULL UNIQUE,
    name                      varchar(120) NOT NULL DEFAULT 'Bambu H2C',
    image_url                 text,
    status                    varchar(16) NOT NULL DEFAULT 'idle'
                              CHECK (status IN ('idle', 'running', 'off', 'error')),
    filaments                 jsonb NOT NULL DEFAULT '[]',
    current_batch_id          uuid REFERENCES batches (id) ON DELETE SET NULL,
    current_layer             integer,
    total_layers              integer,
    batch_total_time_minutes  integer,
    print_started_at          timestamptz,
    total_waste_grams         numeric(10, 2) NOT NULL DEFAULT 0,
    -- Which slicing config (machine_profiles row) this physical unit runs. The
    -- scheduler resolves a batch's machine_id (a machine_profiles id) to a
    -- specific fleet machine by matching this column.
    machine_profile_id       uuid REFERENCES machine_profiles (id) ON DELETE SET NULL,
    -- HMS text from the printer explaining an 'error' status; null otherwise.
    status_reason            text,
    -- What the printer says is left on the plate it is running, and when it
    -- said it. Distinct from print_started_at/batch_total_time_minutes above,
    -- which record what Tensor ASKED this unit to run; these are what it
    -- reports. Readers fence on the observation time - a remaining time from an
    -- unreachable printer is a lie, not a smaller number. See migration 0076.
    remaining_minutes        integer,
    remaining_observed_at    timestamptz,
    -- What the printer is, as BambuBuddy reports it - see migration 0062.
    model                    varchar(64),
    location                 varchar(255),
    ip_address               varchar(64),
    nozzle_count             integer,
    -- What the fixed (external-spool) nozzle holds on a two-nozzle machine, and
    -- which extruder that is, 0-based. The colour is declared by an operator -
    -- an external spool has no RFID, so the printer reports it as 00000000 -
    -- while the index is synced from extruder_slots, where the fixed feed shows
    -- as ams_id 254. Both null on a single-nozzle machine. See migration 0084.
    fixed_nozzle_colour      varchar(16),
    fixed_nozzle_index       integer,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_machines_status ON machines (status);
CREATE INDEX ix_machines_current_batch ON machines (current_batch_id);
CREATE INDEX idx_machines_machine_profile_id ON machines (machine_profile_id);

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

CREATE TABLE production_job_finishing_checks (
    id                uuid PRIMARY KEY,
    job_id            uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    supports_removed  boolean NOT NULL DEFAULT false,
    sanded            boolean NOT NULL DEFAULT false,
    seams_cleaned     boolean NOT NULL DEFAULT false,
    surface_finish_ok boolean NOT NULL DEFAULT false,
    photo_file_id     uuid REFERENCES file_assets (id) ON DELETE SET NULL,
    notes             varchar(1000),
    finished_by       varchar(64) NOT NULL,
    finished_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_finishing_checks_job_id ON production_job_finishing_checks (job_id);

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

-- Number minting for production_jobs.job_number and batches.batch_number (see
-- migration 0033). Sequences rather than random digits: uniqueness becomes
-- structural instead of probabilistic, and the unique indexes above can be
-- relied on rather than merely hoped for.
CREATE SEQUENCE production_job_number_seq START WITH 1000000;
CREATE SEQUENCE batch_number_seq START WITH 1000000;

-- One append-only stream per job (see migration 0035). seq orders events
-- written in the same transaction, which created_at alone cannot.
CREATE TABLE production_job_events (
    id             uuid PRIMARY KEY,
    job_id         uuid NOT NULL REFERENCES production_jobs (id) ON DELETE CASCADE,
    seq            bigserial NOT NULL,
    event_type     varchar(48) NOT NULL,
    stage          varchar(24),
    reason         varchar(64),
    comment        varchar(1000),
    actor_id       varchar(64) NOT NULL,
    batch_id       uuid REFERENCES batches (id) ON DELETE SET NULL,
    related_job_id uuid REFERENCES production_jobs (id) ON DELETE SET NULL,
    metadata       jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_job_events_job ON production_job_events (job_id, seq);
CREATE INDEX ix_job_events_type ON production_job_events (event_type);

-- The product registry: what a product IS, as against what happened to one.
-- See migration 0070. Product -> Option -> Variant -> (Design, BOM); a variant
-- is a COMBINATION of option values, never a product of its own.
CREATE TABLE products (
    id     uuid PRIMARY KEY,
    code   varchar(32)  NOT NULL,
    name   varchar(160) NOT NULL,
    -- 'generated' (Tensor renders it) or 'uploaded' (somebody supplies a 3MF).
    kind   varchar(16)  NOT NULL DEFAULT 'generated',
    status varchar(16)  NOT NULL DEFAULT 'active',
    notes  text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_products_code ON products (lower(code));

CREATE TABLE product_options (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    code       varchar(32)  NOT NULL,
    label      varchar(80)  NOT NULL,
    position   integer      NOT NULL DEFAULT 0,
    created_at timestamptz  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_product_option_code ON product_options (product_id, lower(code));

CREATE TABLE product_option_values (
    id        uuid PRIMARY KEY,
    option_id uuid NOT NULL REFERENCES product_options (id) ON DELETE CASCADE,
    code      varchar(48) NOT NULL,
    label     varchar(80) NOT NULL,
    position  integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_product_option_value_code
    ON product_option_values (option_id, lower(code));

CREATE TABLE product_variants (
    id         uuid PRIMARY KEY,
    product_id uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- Nullable: nine live plank lines carry no SKU and are matched by name.
    sku        varchar(128),
    name       varchar(200) NOT NULL,
    status     varchar(16)  NOT NULL DEFAULT 'active',
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_product_variant_sku
    ON product_variants (lower(sku)) WHERE sku IS NOT NULL;
CREATE INDEX ix_product_variants_product ON product_variants (product_id);

CREATE TABLE product_variant_options (
    variant_id      uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    option_value_id uuid NOT NULL REFERENCES product_option_values (id) ON DELETE CASCADE,
    PRIMARY KEY (variant_id, option_value_id)
);

CREATE TABLE variant_designs (
    id           uuid PRIMARY KEY,
    variant_id   uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    -- 'body' is the product; 'base' is the light box it sits on.
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
CREATE UNIQUE INDEX uq_variant_design_role
    ON variant_designs (variant_id, role) WHERE status = 'active';

CREATE TABLE variant_bom (
    id                uuid PRIMARY KEY,
    variant_id        uuid NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    -- RESTRICT: deleting a part products are built from must fail loudly.
    inventory_item_id uuid NOT NULL REFERENCES inventory_items (id) ON DELETE RESTRICT,
    quantity          numeric(12, 3) NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_variant_bom_item ON variant_bom (variant_id, inventory_item_id);

-- Which order field feeds which OpenSCAD variable, per product - see migration
-- 0079. Product-level, because NAME_L is the same for every colour and a
-- per-variant mapping would be the same rows repeated with somewhere to
-- disagree. property_key is stored normalised, as normalisePropKey produces.
CREATE TABLE product_field_maps (
    id            uuid PRIMARY KEY,
    product_id    uuid NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    property_key  varchar(120) NOT NULL,
    scad_variable varchar(64)  NOT NULL,
    value_type    varchar(16)  NOT NULL DEFAULT 'string',
    -- Which design file this row feeds, matching variant_designs.role - see
    -- migration 0080. A product may print from more than one .scad, and a
    -- combo prints three.
    role          varchar(24)  NOT NULL DEFAULT 'body',
    -- Whether a missing answer holds the job. Required is right for a plank,
    -- where a blank name is scrap; wrong for a rose the customer left unnamed.
    required      boolean      NOT NULL DEFAULT true,
    -- A value that does not come from the order - see migration 0082. The
    -- plank needs OUT_X=200 to reach the product's finished size, and without
    -- it renders at its natural 377 mm: the wrong product, successfully.
    fixed_value   varchar(120),
    position      integer      NOT NULL DEFAULT 0,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT product_field_maps_value_type_check
        CHECK (value_type IN ('string', 'number')),
    -- One source or the other. A row that is half a mapping renders a model
    -- missing what it names, and exits 0.
    CONSTRAINT ck_product_field_map_source CHECK (
        fixed_value IS NOT NULL OR btrim(property_key) <> ''
    )
);
CREATE UNIQUE INDEX uq_product_field_map_variable
    ON product_field_maps (product_id, role, lower(scad_variable));
CREATE INDEX ix_product_field_maps_product
    ON product_field_maps (product_id, role, position);

-- Which BambuBuddy slicer pipeline a SKU prints with, per machine class - see
-- migration 0085. No row means the class default, which is how slicing behaved
-- before this table existed: the first pipeline whose target class matched the
-- printer's model. Keyed on the SKU string because that is what a job carries
-- and what the rule compares; the registry's variants do not hold the SKUs that
-- actually print. Many SKUs may name one pipeline, and the batching token is
-- built from the mapping so that those SKUs keep sharing a plate.
CREATE TABLE sku_slicer_pipelines (
    id             uuid PRIMARY KEY,
    sku            varchar(128) NOT NULL,
    machine_family varchar(16) NOT NULL,
    pipeline_id    integer NOT NULL,
    pipeline_name  varchar(200) NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_sku_pipeline_family
    ON sku_slicer_pipelines (lower(sku), upper(machine_family));

-- An uploaded OpenSCAD template that overrides the embedded one - see migration
-- 0071. No rows means the binary's own templates are used, which is how this
-- behaved before the table existed.
CREATE TABLE design_templates (
    id           uuid PRIMARY KEY,
    template_key varchar(64) NOT NULL,
    file_id      uuid NOT NULL REFERENCES file_assets (id) ON DELETE RESTRICT,
    version      integer     NOT NULL DEFAULT 1,
    status       varchar(16) NOT NULL DEFAULT 'active',
    uploaded_by  varchar(64) NOT NULL,
    notes        varchar(500),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_design_template_active
    ON design_templates (lower(template_key)) WHERE status = 'active';
CREATE INDEX ix_design_templates_key
    ON design_templates (lower(template_key), version DESC);

-- Bulk orders and the quotations they produce. Prices are snapshotted onto each
-- line: a quotation is a number somebody was given on a date. See migration
-- 0088 for the full reasoning.
CREATE TABLE bulk_orders (
    id              uuid PRIMARY KEY,
    quotation_number varchar(32) NOT NULL UNIQUE,
    brand_slug      text NOT NULL REFERENCES brands (slug) ON DELETE RESTRICT,
    customer_name   varchar(200) NOT NULL,
    customer_email  varchar(255),
    customer_phone  varchar(40),
    notes           text,
    order_date      date NOT NULL,
    valid_until     date,
    status          varchar(16) NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'sent', 'accepted', 'cancelled')),
    discount_percent numeric(5, 2) NOT NULL DEFAULT 0
                     CHECK (discount_percent >= 0 AND discount_percent <= 100),
    subtotal        numeric(12, 2) NOT NULL DEFAULT 0,
    discount_amount numeric(12, 2) NOT NULL DEFAULT 0,
    total           numeric(12, 2) NOT NULL DEFAULT 0,
    currency        varchar(8) NOT NULL DEFAULT 'INR',
    created_by      varchar(64),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    -- The code this order's production jobs are numbered from: OMPT gives
    -- OMPT-1, OMPT-2. Set at approval, unique across orders. LAST, because
    -- migration 0091 appends it. See that migration.
    job_code        varchar(16)
);
CREATE INDEX ix_bulk_orders_brand ON bulk_orders (brand_slug, created_at DESC);
CREATE UNIQUE INDEX uq_bulk_orders_job_code ON bulk_orders (upper(job_code)) WHERE job_code IS NOT NULL;

CREATE TABLE bulk_order_lines (
    id             uuid PRIMARY KEY,
    bulk_order_id  uuid NOT NULL REFERENCES bulk_orders (id) ON DELETE CASCADE,
    variant_id     uuid REFERENCES product_variants (id) ON DELETE SET NULL,
    sku            varchar(128) NOT NULL,
    product_name   varchar(300) NOT NULL,
    quantity       integer NOT NULL CHECK (quantity > 0),
    unit_price     numeric(12, 2) NOT NULL CHECK (unit_price >= 0),
    line_total     numeric(12, 2) NOT NULL CHECK (line_total >= 0),
    position       integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- The Shopify product this line belongs to, snapshotted. The workbook has
    -- one sheet per product, and a product is not a SKU. LAST, because
    -- migration 0090 appends it. See that migration for why.
    product_group  varchar(300) NOT NULL DEFAULT ''
);
CREATE INDEX ix_bulk_order_lines_order ON bulk_order_lines (bulk_order_id, position);

-- The coloured pieces one design file prints as.
--
-- Colour has never been configurable. The renderer runs every template twice -
-- PART="base" and PART="text" - paints the first white and the second whatever
-- the customer chose, and assembles the two into a 3MF. That is a plank
-- described in Go: exactly two pieces, the fixed one always white, and no way
-- to say that a rose's heart is red while its stem follows the order.
--
-- A row here is one piece of one product's design file. part_name is BOTH the
-- value passed as -D PART= and the object name in the reference 3MF the parts
-- were read from, deliberately: they have to agree for the render to produce
-- the piece at all, so storing them as one value removes the chance of a
-- mapping between them drifting.
--
-- colour_hex NULL means "whatever the customer chose", and that is why it is a
-- nullable colour rather than a colour plus a boolean: two columns can disagree
-- - fixed with no hex, or a hex that is ignored - and there is no state here
-- that needs them to.
--
-- Products with no rows keep the two-pass behaviour exactly. This is additive
-- in the same way the registry render path is: nothing printing today changes
-- until somebody configures it.
CREATE TABLE IF NOT EXISTS design_colour_parts (
    id         uuid PRIMARY KEY,
    product_id uuid        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    role       varchar(24) NOT NULL DEFAULT 'body',
    part_name  varchar(64) NOT NULL,
    colour_hex varchar(7),
    position   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Rejected at the boundary AND here: a colour that is not #RRGGBB reaches
    -- Write3MF, which refuses it, and a job fails at render time for a typo
    -- made in a form weeks earlier.
    CONSTRAINT ck_design_colour_part_hex CHECK (
        colour_hex IS NULL OR colour_hex ~ '^#[0-9A-Fa-f]{6}$'
    )
);

-- One row per piece per role. Case-insensitive because OpenSCAD string
-- comparison is not, and "Base" and "base" being two rows would render one
-- piece twice and the other never.
CREATE UNIQUE INDEX IF NOT EXISTS uq_design_colour_part
    ON design_colour_parts (product_id, lower(role), lower(part_name));

CREATE INDEX IF NOT EXISTS ix_design_colour_parts_product
    ON design_colour_parts (product_id, role);

-- Credentials an admin can enter, per brand, per provider.
--
-- brand_connections holds ONE token - it was built for OAuth, where that is
-- all there is. A credential-based provider needs several values (a key, a
-- host, ids from somebody else's console), so they do not fit that shape and
-- should not be bent into it.
--
-- Key-value rather than a column per field. The alternative is a migration
-- every time a provider adds a setting, and the set of settings is exactly
-- the thing that keeps moving.
--
-- SECRETS ARE SEALED (internal/secretbox, AES-256-GCM), the same way Shopify's
-- access token is. is_secret says which, so the form knows to mask them.
CREATE TABLE IF NOT EXISTS integration_settings (
    brand_slug varchar(64)  NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    provider   varchar(32)  NOT NULL,
    setting_key varchar(64) NOT NULL,
    -- Sealed when is_secret, plain otherwise.
    setting_value text      NOT NULL,
    is_secret  boolean      NOT NULL DEFAULT false,
    updated_by varchar(64),
    updated_at timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, provider, setting_key)
);

CREATE INDEX IF NOT EXISTS ix_integration_settings_provider
    ON integration_settings (brand_slug, provider);
