-- +goose Up
-- The colour an operator actually recognises.
--
-- Numbered 0043, not 0040: the shared database already has 40, 41 and 42
-- applied from a fork's migration lineage (shopify_api_calls, batch_gcode,
-- print_dispatches), which assigned different migrations to those numbers.
-- goose tracks by version id, so a 0040 here would be silently considered
-- already applied and never run - which is exactly what happened first time.
--
-- filament_inventory.colour is a NAME ("Sky Blue"), which is what the batch
-- planner keys on and must stay. But a name is not a colour: an operator
-- scanning a shelf matches the swatch, and BambuBuddy shows one. Storing the
-- hex alongside lets Tensor render the same swatch instead of asking the reader
-- to imagine "Ivory".
--
-- Nullable on purpose: rows added by hand have no hex, and a made-up default
-- would render a confidently wrong colour.
ALTER TABLE filament_inventory ADD COLUMN IF NOT EXISTS colour_hex varchar(9);

-- +goose Down
ALTER TABLE filament_inventory DROP COLUMN IF EXISTS colour_hex;
