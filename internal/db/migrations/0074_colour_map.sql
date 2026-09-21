-- What a colour name means to the printers that actually have to print it.
--
-- An order names a colour in words - "BLUE", "SKY BLUE". An AMS reports one as
-- a bare hex and nothing else: tray_sub_brands is empty on every tray in this
-- fleet and there is no name field at all. Something has to hold both, and
-- until now nothing did.
--
-- The consequence was not subtle. resolveColourHex fell through to the built-in
-- table in dnp_colour.go, which calls blue #1560BD, while the spools in these
-- machines report #2850E0. That hex is written into the plate's
-- project_settings.config, which is the ONLY colour signal BambuBuddy reads, so
-- it refused plate after plate with "filament (slot 2): needs #1560BD, has
-- #46A8F9" - Tensor asking for a colour nobody owns.
--
-- Matching on colour NAME does not fix it either: 11 of the 14 hexes loaded
-- across this fleet are absent from BambuBuddy's own colour catalogue, because
-- the spools are generic rather than Bambu. The only ground truth is the tray.
--
-- A name accepts MANY hexes, exactly one of them primary. Thirteen printers do
-- not agree on blue. One row per name would force the exact-match check in
-- missingColours - which is deliberately exact, and must stay exact, because a
-- near-enough colour is a scrapped plank - to reject every machine holding the
-- other blue. The primary is the one rendering and the dialog swatch use; the
-- rest are alternatives a machine may legitimately hold.
--
-- Deliberately NOT a column on filament_inventory, which already carries a
-- colour_hex. Two mechanisms there would destroy this:
--   * SetFilamentStockFromSource writes colour_hex = COALESCE(narg, colour_hex),
--     so the first spool sync that returns anything overwrites an operator's
--     confirmed hex with the catalogue value already known to be wrong;
--   * DeleteFilamentNotIn drops the whole row once the shelf stops reporting
--     that material and colour, so the mapping for BLUE would disappear because
--     the last blue spool ran out.
-- A statement about what a colour IS must not be a side effect of a stock
-- refresh.
--
-- confirmed_by holds a Better Auth user id with no foreign key, matching
-- user_roles: an orphaned row is harmless, and knowing who stood in front of
-- the printer is the point.

-- +goose Up
CREATE TABLE IF NOT EXISTS colour_map (
    id           uuid PRIMARY KEY,
    -- Canonical and uppercase, as production.CanonicalColourName returns, so
    -- SKY BLUE and BLUE resolve to one spool.
    colour_name  varchar(64) NOT NULL,
    -- '#RRGGBB', uppercase. Exactly what the AMS reports for this spool.
    hex          varchar(7) NOT NULL,
    is_primary   boolean NOT NULL DEFAULT false,
    note         text,
    confirmed_by varchar(255),
    confirmed_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- One row per (name, hex) pair, case- and whitespace-insensitively.
CREATE UNIQUE INDEX IF NOT EXISTS uq_colour_map_pair
    ON colour_map (lower(trim(colour_name)), upper(hex));

-- At most one primary per name, enforced here rather than in Go: two writers
-- confirming a spool at once must not leave a colour with two primaries, and a
-- partial unique index is the only place that can be guaranteed.
CREATE UNIQUE INDEX IF NOT EXISTS uq_colour_map_primary
    ON colour_map (lower(trim(colour_name))) WHERE is_primary;

-- +goose Down
DROP INDEX IF EXISTS uq_colour_map_primary;
DROP INDEX IF EXISTS uq_colour_map_pair;
DROP TABLE IF EXISTS colour_map;
