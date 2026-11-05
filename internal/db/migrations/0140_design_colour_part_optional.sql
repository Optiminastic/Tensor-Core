-- +goose Up
-- A piece that only some orders have.
--
-- Every configured piece is mandatory today, and that is the right default: a
-- piece whose name the template does not branch on renders an empty solid,
-- which slices and prints and is simply missing a part. renderPieces holds the
-- job and names the piece, which is how a typo in a scheme gets found.
--
-- But some pieces are genuinely conditional. The Social Mirror's blue verified
-- tick is drawn only when the order asks for one, so `PART="tick"` on an order
-- without a tick writes no file at all - OpenSCAD exits 0 and produces nothing.
-- Configuring the mirror's four pieces would therefore fail every order that
-- did not buy a tick, and the designer saw it coming:
--
--     // No TICK_COLOR on purpose: an order without a tick renders an empty
--     // tick piece, and Tensor holds a job with an empty piece.
--
-- The alternative to this column is to leave the mirror unconfigured, which is
-- where it is now: it falls back to the plank's two pieces, so it prints with
-- no red heart and no blue tick at all.
--
-- A FLAG PER PIECE, not a looser rule for everyone. "An empty piece is fine"
-- applied globally would bring back the bug the guard was written for - a plank
-- whose lettering rendered nothing coming out blank and reporting success. The
-- piece that may be absent is a fact about the design, so the design says which
-- one it is, and every other piece keeps failing loudly.
ALTER TABLE design_colour_parts
    ADD COLUMN IF NOT EXISTS optional boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE design_colour_parts DROP COLUMN IF EXISTS optional;
