-- +goose Up
-- Renumbered from 0094. The number matters more than it looks.
--
-- These two were written before 0095_integration_settings and pushed after
-- it, which puts them BELOW the version every deployed database is already
-- at. goose.Up refuses that: it collects every on-disk migration below the
-- recorded max version that has not been applied and returns
--
--   error: found 2 missing migrations before current version 95
--
-- applying nothing. internal/db/migrate.go calls goose.Up with no options, so
-- there is no WithAllowMissing to soften it, and cmd/api migrates on boot -
-- the API would simply not start. Verified against goose v3.27.2's
-- findMissingMigrations, which only looks at files on disk, so the reverse
-- case (a recorded version whose file is gone) is harmless.
--
-- When the win-back scheduler started watching each store.
--
-- WITHOUT THIS, THE FIRST SWEEP IS A BACKLOG. Shopify's abandoned list is
-- everything from the last thirty days, so a worker starting for the first
-- time - or on a fresh deployment, or after the table was cleared - looks at
-- hundreds of carts that have been sitting there for weeks and starts dialling
-- them. Every one of those people abandoned a basket long ago and has heard
-- nothing since; a call now is a cold call, not a win-back.
--
-- So the first sweep for a brand records the moment it looked, and from then
-- on only checkouts created AFTER that moment are eligible. The backlog is
-- skipped once and forever, rather than skipped by a rule somebody can relax.
--
-- One row per brand, because stores are connected at different times and a
-- single global mark would silence a newly connected one.
CREATE TABLE IF NOT EXISTS winback_watermarks (
    brand_slug varchar(64)  PRIMARY KEY,
    -- Only checkouts created strictly after this are considered.
    watch_from timestamptz  NOT NULL,
    created_at timestamptz  NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS winback_watermarks;
