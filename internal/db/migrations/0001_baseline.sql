-- +goose Up
-- Baseline schema for the tables Tensor-Core owns. This reproduces the exact
-- state of the Python backend's Alembic head (revision e3b9d5a71c04) so the Go
-- backend takes over the same database with no drift.
--
-- It is written idempotently (IF NOT EXISTS, guarded enum creation) so it is a
-- safe no-op on the existing shared database -- where these tables already
-- exist -- and builds everything on a fresh database. It never creates, alters
-- or drops Better Auth's tables (user, session, account, verification, jwks);
-- those are owned by the Better Auth CLI from the frontend.

-- +goose StatementBegin
DO $$ BEGIN
    CREATE TYPE brand AS ENUM ('gifting', 'decor');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ BEGIN
    CREATE TYPE project_status AS ENUM ('active', 'archived');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS cost_assumption_sets (
    id                        uuid PRIMARY KEY,
    name                      varchar(120) NOT NULL UNIQUE,
    brand                     brand,
    filament_cost_per_kg      numeric(10, 2) NOT NULL,
    electricity_cost_per_unit numeric(10, 2) NOT NULL,
    machine_hour_cost         numeric(10, 2) NOT NULL,
    finishing_labour          numeric(10, 2) NOT NULL,
    consumables               numeric(10, 2) NOT NULL,
    failure_pct               numeric(5, 4) NOT NULL,
    is_default                boolean NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS machine_profiles (
    id                uuid PRIMARY KEY,
    name              varchar(120) NOT NULL UNIQUE,
    machine_hour_cost numeric(10, 2) NOT NULL,
    is_active         boolean NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS material_profiles (
    id            uuid PRIMARY KEY,
    name          varchar(120) NOT NULL UNIQUE,
    material_type varchar(40) NOT NULL,
    cost_per_kg   numeric(10, 2) NOT NULL,
    colour        varchar(60),
    is_active     boolean NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
    id          uuid PRIMARY KEY,
    name        varchar(40) NOT NULL UNIQUE,
    description varchar(200) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS permissions (
    id          uuid PRIMARY KEY,
    resource    varchar(40) NOT NULL,
    action      varchar(40) NOT NULL,
    description varchar(200) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_permission_resource_action UNIQUE (resource, action)
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permissions (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id     varchar(64) NOT NULL,
    role_id     uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    assigned_by varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX IF NOT EXISTS ix_user_roles_user_id ON user_roles (user_id);

CREATE TABLE IF NOT EXISTS user_authz_state (
    user_id             varchar(64) PRIMARY KEY,
    permissions_version integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_invites (
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
CREATE INDEX IF NOT EXISTS ix_user_invites_email ON user_invites (email);

CREATE TABLE IF NOT EXISTS projects (
    id          uuid PRIMARY KEY,
    name        varchar(120) NOT NULL UNIQUE,
    brand       brand NOT NULL,
    description varchar(500),
    status      project_status NOT NULL,
    created_by  varchar(64) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS brands (
    id                  uuid PRIMARY KEY,
    key                 brand NOT NULL UNIQUE,
    name                varchar(120) NOT NULL,
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

-- +goose Down
-- Drops only Tensor-Core's tables and enums. Better Auth's tables are untouched.
DROP TABLE IF EXISTS brands;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS user_invites;
DROP TABLE IF EXISTS user_authz_state;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS material_profiles;
DROP TABLE IF EXISTS machine_profiles;
DROP TABLE IF EXISTS cost_assumption_sets;
DROP TYPE IF EXISTS project_status;
DROP TYPE IF EXISTS brand;
