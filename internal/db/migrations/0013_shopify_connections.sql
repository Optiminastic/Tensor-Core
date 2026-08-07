-- +goose Up
-- Phase 4: inbound Shopify. One app installed across many stores; per store we
-- keep an encrypted offline access token and the orders/paid webhook id. Orders
-- imported by the webhook link back to their store connection (the FK left open
-- in 0010 is wired here), and order numbers are unique per store. Written
-- idempotently.

CREATE TABLE IF NOT EXISTS shopify_connections (
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
-- At most one active connection per store domain.
CREATE UNIQUE INDEX IF NOT EXISTS ix_shopify_connections_active_domain
    ON shopify_connections (shop_domain) WHERE is_active;

-- Wire orders.shop_connection_id (added FK-less in 0010) to the connection.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_orders_shop_connection_id'
    ) THEN
        ALTER TABLE orders
            ADD CONSTRAINT fk_orders_shop_connection_id
            FOREIGN KEY (shop_connection_id) REFERENCES shopify_connections (id) ON DELETE SET NULL;
    END IF;
END $$;
-- +goose StatementEnd

-- Order numbers are unique within a store (NULL connection rows - e.g. seeded
-- test orders - are treated as distinct by the index and do not collide).
CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_shop_order_number
    ON orders (shop_connection_id, order_number);

-- +goose Down
DROP INDEX IF EXISTS uq_orders_shop_order_number;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_shop_connection_id;
DROP TABLE IF EXISTS shopify_connections;
