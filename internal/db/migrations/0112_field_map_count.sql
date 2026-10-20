-- +goose Up
-- A mapped field may be a COUNT read out of the storefront's wording.
--
-- The registry could map a customer's answer as a string or as a number, and
-- neither reads a heart count. The storefront does not sell "1" - it sells
-- "1 RED HEART", and it renames the option freely to "2 PINK HEARTS" or
-- "NO HEART". Mapped as a string that reaches OpenSCAD as side_hearts="1 RED
-- HEART", which is not a number and fails every comparison the script makes
-- against it; mapped as a number it is refused outright and the job is held.
--
-- That gap is why the Dual Name Plank - the highest-volume product in the shop
-- - still renders from Go rather than from the registry. templateForHearts and
-- heartCount know how to read "1 RED HEART"; the registry had no way to say
-- it, so the plank could not be described in the registry at all, and every
-- change to it has needed a deploy.
--
-- 'count' says it: read the first run of digits, and treat wording that names
-- no number but says none - "NO HEART", "none", "without heart" - as zero.
-- Anything else is refused, exactly as a number is, because a count guessed
-- from wording nobody anticipated prints the wrong product and exits 0.
--
-- The value still reaches OpenSCAD UNQUOTED, like a number, because that is
-- what side_hearts is compared against arithmetically in the template.
ALTER TABLE product_field_maps
    DROP CONSTRAINT IF EXISTS product_field_maps_value_type_check;

ALTER TABLE product_field_maps
    ADD CONSTRAINT product_field_maps_value_type_check
        CHECK (value_type IN ('string', 'number', 'count'));

-- A mapped field may carry a DEFAULT for when the order does not answer it.
--
-- An optional field that the order leaves blank is simply not passed, so the
-- template's own default stands. That is right for a rose the customer did not
-- name; it is wrong for the plank's heart count, and dangerously so. The Go
-- path reads "nothing on this line names hearts" as zero, because the 2-STEP
-- plank does not sell them - ten live lines are that shape. The joinery
-- template defaults side_hearts to 1. Mapped as optional, every one of those
-- orders would quietly grow a heart nobody bought.
--
-- fixed_value could not express it: a row carrying one ignores the order
-- entirely, so it is "always this", not "this when unanswered". Hence a
-- column of its own rather than a reinterpretation of that one, which would
-- change what every existing fixed row means.
--
-- Typed like any other value: a default on a 'number' or 'count' row reaches
-- OpenSCAD unquoted and must parse, which is checked where it is used.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS default_value varchar(120);

-- +goose Down
ALTER TABLE product_field_maps
    DROP COLUMN IF EXISTS default_value;

-- Rows using the new type would violate the narrower constraint, so they are
-- put back to 'string' rather than blocking the rollback. That is lossy and
-- deliberately so: a rolled-back schema has no code that understands 'count',
-- and a row left claiming it would be read as a string by whatever is running.
UPDATE product_field_maps SET value_type = 'string' WHERE value_type = 'count';

ALTER TABLE product_field_maps
    DROP CONSTRAINT IF EXISTS product_field_maps_value_type_check;

ALTER TABLE product_field_maps
    ADD CONSTRAINT product_field_maps_value_type_check
        CHECK (value_type IN ('string', 'number'));
