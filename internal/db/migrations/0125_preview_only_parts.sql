-- +goose Up
-- Removes design_colour_parts.printed, which existed for about an hour.
--
-- It was a flag on a PIECE: "drawn in the customer's picture, not sent to the
-- bed". Written for the lotus holder, whose one .scad draws both the plate and
-- the diya cup that pushes onto it, where marking the cup was enough.
--
-- It is the wrong shape for the product that comes next. A Royal diya holder's
-- assembled form needs a foot and a cup its print file does not contain, so
-- there is no piece to flag - the picture needs a DIFFERENT FILE. That is a
-- design role, which variant_designs already expresses, and a product cannot
-- be made to have two mechanisms for one idea without one of them rotting.
--
-- So the picture is role 'preview' throughout, and this column is gone.
--
-- IDEMPOTENT AND MOSTLY A NO-OP. The column was only ever added by an earlier
-- version of this same file, on one laptop; production never saw it. The file
-- is kept rather than deleted so a database that did see it is brought back
-- into line, and so the migration numbers stay contiguous.
ALTER TABLE design_colour_parts DROP COLUMN IF EXISTS printed;

-- +goose Down
-- Deliberately empty. Restoring the column would restore nothing that reads
-- it.
SELECT 1;
