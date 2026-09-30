-- +goose Up
-- Which BambuBuddy slicer pipeline a SKU prints with, per machine class.
--
-- Every plate is sliced with settings chosen by PRINTER MODEL alone:
-- pipelineForModel keeps the pipelines whose target_model_class matches and
-- takes the first. So a Dual Name Plank and a heart keychain sent to the same
-- H2C are sliced identically, and with two H2C pipelines on this floor, which
-- one they get is whichever BambuBuddy happens to list first.
--
-- Keyed on the SKU STRING, not on a product_variants row, and that is not
-- laziness about foreign keys. The live jobs and the registry disagree about
-- SKUs: production_jobs carries DNPWL-GLD, DNP-GLD, DNPWL-BLK, while the DNP
-- variants in the registry carry no SKU at all and are named "1 heart, No
-- light". Of eight SKUs printing today, one matches a variant row. A mapping
-- hung off variant_id would therefore reach almost nothing that actually
-- prints, and would go on doing so until somebody re-imported the catalogue.
--
-- The slice path compares the job's SKU anyway, so this stores exactly what the
-- rule uses. A SKU that later gains a variant row keeps its mapping.
--
-- Many SKUs may share one pipeline, which is the common case: the pipeline is
-- referenced by id, so any number of rows may name the same one. That is also
-- what lets those SKUs keep sharing a plate - the batching token is built from
-- the MAPPING rather than from the SKU.
CREATE TABLE IF NOT EXISTS sku_slicer_pipelines (
    id             uuid PRIMARY KEY,
    sku            varchar(128) NOT NULL,
    -- H2C / A2L / P2S. One row per class, so a SKU printed on all three has
    -- three rows and a SKU with none falls back to the class default.
    machine_family varchar(16) NOT NULL,
    pipeline_id    integer NOT NULL,
    -- A snapshot, not a join key. A pipeline deleted in BambuBuddy has to
    -- produce "the pipeline this SKU used is gone" rather than a silent
    -- fallback to somebody else's settings, and only a stored name can say
    -- which one it was.
    pipeline_name  varchar(200) NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive on both parts: a SKU typed into Shopify and one typed into
-- Tensor disagree about case more often than anybody expects, and two rows for
-- one SKU would make which settings apply a matter of query order.
CREATE UNIQUE INDEX IF NOT EXISTS uq_sku_pipeline_family
    ON sku_slicer_pipelines (lower(sku), upper(machine_family));

-- +goose Down
DROP INDEX IF EXISTS uq_sku_pipeline_family;
DROP TABLE IF EXISTS sku_slicer_pipelines;
