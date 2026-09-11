-- +goose Up
-- An uploaded OpenSCAD template that overrides the one compiled into the binary.
--
-- The three plank templates are //go:embed-ed, which makes them fast and
-- impossible to lose - and impossible to change without a developer, a
-- recompile and a deploy. templates/README.md records the cost of that plainly:
-- the embedded copies drifted five days behind the shop's masters and nobody
-- noticed until the file sizes were compared.
--
-- This is the seam. A row here names a template key and the file that should be
-- used for it; the renderer prefers the upload and falls back to the embedded
-- copy when there is none. So an install with no rows behaves exactly as it does
-- today, and uploading a file is what changes behaviour - never a deploy.
--
-- Versioned rather than overwritten. A template decides the shape of every plank
-- printed from it, so "what did we print last Tuesday" has to stay answerable
-- after somebody uploads a fix. Only one version per key is active at a time.
CREATE TABLE IF NOT EXISTS design_templates (
    id           uuid PRIMARY KEY,
    -- The key the renderer asks for: "dnp_two_heart", "dual_one_heart".
    -- Matches the embedded filename without its extension, so an upload can
    -- shadow an embedded template without anything else being renamed.
    template_key varchar(64) NOT NULL,
    file_id      uuid NOT NULL REFERENCES file_assets (id) ON DELETE RESTRICT,
    version      integer     NOT NULL DEFAULT 1,
    -- 'active' or 'superseded'. Kept rather than deleted: see the versioning
    -- note above.
    status       varchar(16) NOT NULL DEFAULT 'active',
    uploaded_by  varchar(64) NOT NULL,
    notes        varchar(500),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One active template per key, enforced rather than trusted: two active rows
-- would make which file renders a plank depend on row order.
CREATE UNIQUE INDEX IF NOT EXISTS uq_design_template_active
    ON design_templates (lower(template_key)) WHERE status = 'active';

CREATE INDEX IF NOT EXISTS ix_design_templates_key
    ON design_templates (lower(template_key), version DESC);

-- +goose Down
DROP INDEX IF EXISTS ix_design_templates_key;
DROP INDEX IF EXISTS uq_design_template_active;
DROP TABLE IF EXISTS design_templates;
