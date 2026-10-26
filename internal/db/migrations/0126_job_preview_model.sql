-- +goose Up
-- The model the CUSTOMER is shown, when it differs from the one that prints.
--
-- Until now there was one model per job and the preview was a picture of it,
-- which was right while every piece of every product went on the bed. The
-- lotus holder broke that: its diya cup is a separate print that pushes onto
-- the finished plate, so the plate that goes to batching must not carry it
-- and the picture the customer approves must.
--
-- No row is the ordinary case and means exactly what it used to: the picture
-- is a picture of print_file_id. Only a product with preview-only pieces gets
-- one, so nothing changes for the planks.
--
-- A SIDE TABLE, not a column on production_jobs. That is where this belongs
-- by meaning, and it is not where it can go: a dozen generated row types are
-- converted to gen.ProductionJob structurally, and a new column on the table
-- breaks every one of them. Code with no interest in previews would pay for
-- this; a join pays for it here instead.
CREATE TABLE IF NOT EXISTS job_preview_models (
    job_id     uuid PRIMARY KEY REFERENCES production_jobs (id) ON DELETE CASCADE,
    file_id    uuid NOT NULL REFERENCES file_assets (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS job_preview_models;
