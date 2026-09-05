-- +goose Up
-- Numbered 0058, not 0044.
--
-- Versions 40-57 are already recorded as applied in the shared Neon
-- database - 44-57 on 2026-08-29 - by a FORK's migration lineage that the
-- Coolify deployment runs against the same database. goose only compares
-- version numbers, so a file numbered below the current version is skipped
-- in silence: `go run ./cmd/migrate` reports "no migrations to run" and the
-- column never appears. Verified: this file as 0044 did exactly that.
--
-- Check `select max(version_id) from goose_db_version` before adding the
-- next one; the fork can move the high-water mark again at any time.
-- Why a batch never reached the printer.
--
-- printBatch returned upload and queue failures to the HTTP caller and stored
-- nothing, so a batch that failed to reach BambuBuddy was indistinguishable
-- from one nobody had tried to send. The operator who pressed the button saw
-- the error once; everyone after them saw a Locked batch sitting there.
--
ALTER TABLE batches ADD COLUMN IF NOT EXISTS print_error text;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS print_error_at timestamptz;

-- BambuBuddy's queue item id, so a batch can be followed after it is sent
-- rather than only at the moment of sending.
ALTER TABLE batches ADD COLUMN IF NOT EXISTS queue_item_id integer;

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS print_error;
ALTER TABLE batches DROP COLUMN IF EXISTS print_error_at;
ALTER TABLE batches DROP COLUMN IF EXISTS queue_item_id;
