-- +goose Up
-- Columns the shop adds to its own inventory shelves.
--
-- The stock sheet the floor keeps carries more than Tensor models: a box's
-- size, a spool count's opening and used figures, who took one. Rather than a
-- schema change per column somebody wants, each shelf has a list of named
-- columns, and each row keeps its values for them in `extra`, keyed by the
-- column's name. Values are text: these are notes an operator reads, never a
-- figure the planner computes with.
CREATE TABLE IF NOT EXISTS inventory_custom_columns (
    id         uuid PRIMARY KEY,
    shelf      varchar(16) NOT NULL CHECK (shelf IN ('filament', 'items')),
    name       varchar(64) NOT NULL,
    position   integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_custom_column
    ON inventory_custom_columns (shelf, lower(name));

ALTER TABLE filament_inventory ADD COLUMN IF NOT EXISTS extra jsonb NOT NULL DEFAULT '{}';
ALTER TABLE inventory_items ADD COLUMN IF NOT EXISTS extra jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE inventory_items DROP COLUMN IF EXISTS extra;
ALTER TABLE filament_inventory DROP COLUMN IF EXISTS extra;
DROP TABLE IF EXISTS inventory_custom_columns;
