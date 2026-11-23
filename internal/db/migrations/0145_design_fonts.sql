-- +goose Up
-- Font files uploaded for design templates.
--
-- OpenSCAD does not fail on a missing font: fontconfig substitutes another
-- face and the render reports success. The Duo Snap's file names "Ondfuturs",
-- the worker had no such face, and JOB-116894-23 was built in a substitute.
-- Until now the only way to add a face was a commit and a redeploy.
--
-- A row here is one uploaded .ttf/.otf. `families` holds every name a template
-- can use for it, lower-cased (the font's family and, when it declares one,
-- its typographic family - "segoe ui black" and "segoe ui"). A render stages
-- only the faces whose families its template names, and only for families no
-- worker ships, so templates that render today are untouched.
--
-- The file is identified by its content: the same font uploaded twice is one
-- row.
CREATE TABLE IF NOT EXISTS design_fonts (
    id           uuid PRIMARY KEY,
    families     text[] NOT NULL,
    style        varchar(128) NOT NULL DEFAULT '',
    filename     varchar(255) NOT NULL,
    storage_key  varchar(512) NOT NULL,
    sha256       char(64) NOT NULL,
    size_bytes   bigint NOT NULL,
    uploaded_by  varchar(255) NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT design_fonts_families_not_empty CHECK (cardinality(families) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_design_fonts_sha256 ON design_fonts (sha256);
CREATE INDEX IF NOT EXISTS ix_design_fonts_families ON design_fonts USING gin (families);

-- +goose Down
DROP INDEX IF EXISTS ix_design_fonts_families;
DROP INDEX IF EXISTS uq_design_fonts_sha256;
DROP TABLE IF EXISTS design_fonts;
