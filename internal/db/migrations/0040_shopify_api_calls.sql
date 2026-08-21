-- +goose Up
-- An audit log of every request Tensor makes to a store's Shopify Admin API.
-- One row per physical HTTP request (retries included), recorded best-effort by
-- the Shopify client so it never blocks a publish. `operation` is the GraphQL
-- field invoked (e.g. productCreate) and `scope` is the access scope that
-- operation exercises (e.g. write_products), so a reviewer can see exactly what
-- the app does to a store and under which permission.
CREATE TABLE IF NOT EXISTS shopify_api_calls (
    id            uuid PRIMARY KEY,
    shop_domain   varchar(255) NOT NULL,
    brand_slug    varchar(255),
    method        varchar(8)   NOT NULL,
    operation     varchar(128) NOT NULL,
    scope         varchar(64)  NOT NULL DEFAULT '',
    status_code   integer      NOT NULL DEFAULT 0,
    ok            boolean      NOT NULL DEFAULT false,
    error_message text,
    latency_ms    integer      NOT NULL DEFAULT 0,
    created_at    timestamptz  NOT NULL DEFAULT now()
);

-- Keyset pagination: newest first, id as a stable tiebreaker.
CREATE INDEX IF NOT EXISTS ix_shopify_api_calls_created ON shopify_api_calls (created_at DESC, id DESC);
-- Filter by store, newest first.
CREATE INDEX IF NOT EXISTS ix_shopify_api_calls_shop ON shopify_api_calls (shop_domain, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS shopify_api_calls;
