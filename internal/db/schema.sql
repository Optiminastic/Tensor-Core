-- Plain DDL used ONLY by sqlc to build its type catalog for code generation.
-- The runtime source of truth is internal/db/migrations/0001_baseline.sql
-- (idempotent, goose). These two must describe the same schema; the DB
-- integration tests apply the goose migration and then run every sqlc query, so
-- any drift between them fails the tests.

-- Defined as domains (not enums) for codegen ONLY: sqlc maps a domain to its
-- base type (text -> Go string) and generates no enum type, avoiding a clash
-- between the `brand` enum's type and the `brands` table model. The real
-- database (goose migration) uses genuine ENUM types of the same name; every
-- query casts text <-> the enum, so this codegen shim and the runtime type line
-- up. Keep the allowed values in sync with 0001_baseline.sql.
CREATE DOMAIN brand AS text CHECK (VALUE IN ('gifting', 'decor'));
CREATE DOMAIN project_status AS text CHECK (VALUE IN ('active', 'archived'));

CREATE TABLE cost_assumption_sets (
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

CREATE TABLE projects (
    id          uuid PRIMARY KEY,
    name        varchar(120) NOT NULL UNIQUE,
    brand       brand NOT NULL,
    description varchar(500),
    status      project_status NOT NULL,
    created_by  varchar(64) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE brands (
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
