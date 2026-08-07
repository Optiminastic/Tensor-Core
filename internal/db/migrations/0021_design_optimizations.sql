-- Cache of the AI optimization advisor's report per design + input signature.
-- The result is stored as jsonb so the report shape can grow without a migration;
-- input_hash keys it to the exact machine/metrics the advice was based on, so a
-- re-slice (new metrics -> new hash) produces fresh advice while a repeat request
-- for the same inputs is served free from cache. ids are app-generated (google/uuid).

-- +goose Up
CREATE TABLE IF NOT EXISTS design_optimizations (
    id         uuid PRIMARY KEY,
    design_id  uuid NOT NULL REFERENCES designs (id) ON DELETE CASCADE,
    input_hash text NOT NULL,
    result     jsonb NOT NULL,
    model      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (design_id, input_hash)
);

-- +goose Down
DROP TABLE IF EXISTS design_optimizations;
