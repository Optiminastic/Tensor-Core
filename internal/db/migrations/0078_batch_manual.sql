-- +goose Up
-- A bed somebody built by hand, which the planner must not overrule.
--
-- The planner treats every Draft as its own proposal: each run reconsiders the
-- jobs sitting on Drafts, dissolves those beds (DeleteDraftBatches) and rebuilds
-- them from scratch. That is right for beds it proposed, and it runs on a
-- seven-minute timer as well as on several events - so a bed assembled by a
-- person would survive minutes at most before being taken apart and its planks
-- redistributed, with nothing on screen to say why.
--
-- The same principle the scheduler already applies one step later, where an
-- approved batch "is a human commitment and is left alone for a human to move
-- manually". This says a hand-built bed is a commitment at Draft too.
--
-- Still an ordinary Draft in every other way: no filament reserved, no plate
-- promised, approved and locked through the same path, deletable by whoever
-- built it. The flag withholds it from ONE actor.
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS manual boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS manual;
