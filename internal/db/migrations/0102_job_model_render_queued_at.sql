-- +goose Up
-- When a model was queued to be rebuilt, and therefore when the one on file is
-- known to be older than the order.
--
-- THE RACE THIS CLOSES. A customer has thirty minutes to correct their order.
-- A colour-only change is settled before the request returns (recolourModel
-- swaps a hex in the stored 3MF), but a NAME change only enqueues a render -
-- while the new names, colour and `colours` are written immediately. Nothing
-- in the release paths waited for that render. So a name corrected at T-30s
-- was released at T by the expiry sweep, which fires a replan, and the planner
-- packed the job from the OLD print file's bbox while grouping it by the NEW
-- colour. A bed could lock and dispatch in the same pass.
--
-- It self-healed only through replateBatchAfterRender, with no ordering
-- guarantee against the plate actually being started. That is not a race worth
-- keeping: the whole point of the window is that what prints is what the
-- customer last approved.
--
-- A TIMESTAMP, NOT A BOOLEAN. A worker that dies between enqueueing a render
-- and storing its result would hold a job for ever, and "true" gives nothing
-- to find it by. An age does: the sweep clears anything older than a few
-- minutes and says so out loud. Renders are now one to two seconds, so a
-- stalled hold is a fault rather than a slow day.
--
-- Read by the three queries that admit a job to batching, never by the release
-- paths. Gating the release instead looks equivalent and strands orders: the
-- expiry sweep releases and expires the link in the same pass and only ever
-- walks expired links, so a job it skipped is never revisited.
ALTER TABLE production_jobs
    ADD COLUMN IF NOT EXISTS model_render_queued_at timestamptz;

-- Partial, because the column is NULL for almost every row almost always -
-- a render in flight is a handful of jobs for a second or two, and the planner
-- asks "is this NULL" on every pass.
CREATE INDEX IF NOT EXISTS ix_production_jobs_render_pending
    ON production_jobs (model_render_queued_at)
    WHERE model_render_queued_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_production_jobs_render_pending;
ALTER TABLE production_jobs DROP COLUMN IF EXISTS model_render_queued_at;
