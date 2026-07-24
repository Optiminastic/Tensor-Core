-- +goose Up
-- The design pipeline: a designer uploads an STL, the slicer worker turns it into
-- physical metrics, and the cost engine turns those into a Design CP + verdict.
-- Written idempotently (IF NOT EXISTS) so it is safe to re-run.

-- A design: the uploaded model plus the answers that drive slicing. Its status is
-- the lifecycle: queued -> slicing -> priced (or failed).
CREATE TABLE IF NOT EXISTS designs (
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
CREATE INDEX IF NOT EXISTS ix_designs_brand_slug ON designs (brand_slug);

-- One slice attempt for a design. Resubmitting creates a new job, so a design can
-- have several; the latest done job carries the current metrics.
CREATE TABLE IF NOT EXISTS slice_jobs (
    id         uuid PRIMARY KEY,
    design_id  uuid NOT NULL REFERENCES designs (id) ON DELETE CASCADE,
    status     text NOT NULL,
    attempt    integer NOT NULL DEFAULT 1,
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_slice_jobs_design_id ON slice_jobs (design_id);

-- The physical facts a slice produced, one row per job. Per-unit values (after
-- batching units_per_bed on a bed); the extras below feed suggestions, not cost.
CREATE TABLE IF NOT EXISTS slice_metrics (
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

-- The costed result for a design: Design CP, the itemised breakdown, the
-- Green/Yellow/Red verdict, and the fix suggestions. One row per design, replaced
-- on each successful re-slice.
CREATE TABLE IF NOT EXISTS design_pricing (
    design_id      uuid PRIMARY KEY REFERENCES designs (id) ON DELETE CASCADE,
    design_cp      numeric(12, 2) NOT NULL,
    breakdown      json NOT NULL,
    verdict        text NOT NULL,
    cp_pct         numeric(6, 4) NOT NULL,
    recommended_sp integer,
    reasons        json NOT NULL,
    suggestions    json NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS design_pricing;
DROP TABLE IF EXISTS slice_metrics;
DROP TABLE IF EXISTS slice_jobs;
DROP TABLE IF EXISTS designs;
