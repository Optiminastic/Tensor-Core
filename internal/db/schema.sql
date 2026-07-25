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
