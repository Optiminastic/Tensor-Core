-- +goose Up
-- The physical printer fleet, distinct from machine_profiles (which is a
-- printer *model*/slicing profile - name, nozzle, cost - config data that
-- rarely changes). This table is one row per physical unit on the floor and
-- carries live print-state: what it's printing right now, how far through,
-- what's loaded in its AMS, and how much it has wasted overall.
--
-- remaining_seconds is not ticked down by anything in this codebase yet - it
-- is paired with print_started_at so a live countdown can always be computed
-- as batch_total_time_minutes*60 - (now() - print_started_at), rather than
-- trusting a stored number that goes stale the moment nothing updates it.
CREATE TABLE IF NOT EXISTS machines (
    id                        uuid PRIMARY KEY,
    machine_id                varchar(64) NOT NULL UNIQUE,
    name                      varchar(120) NOT NULL DEFAULT 'Bambu H2C',
    image_url                 text,
    status                    varchar(16) NOT NULL DEFAULT 'idle'
                              CHECK (status IN ('idle', 'running', 'off')),
    -- One entry per loaded spool: [{"colour": "White", "type": "PLA Basics",
    -- "remaining_grams": 850.0}, ...] - up to 4 for an AMS-equipped unit.
    filaments                 jsonb NOT NULL DEFAULT '[]',
    current_batch_id          uuid REFERENCES batches (id) ON DELETE SET NULL,
    current_layer             integer,
    total_layers              integer,
    batch_total_time_minutes  integer,
    print_started_at          timestamptz,
    total_waste_grams         numeric(10, 2) NOT NULL DEFAULT 0,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_machines_status ON machines (status);
CREATE INDEX IF NOT EXISTS ix_machines_current_batch ON machines (current_batch_id);

-- +goose Down
DROP TABLE IF EXISTS machines;
