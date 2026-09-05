-- +goose Up
-- Which BambuBuddy slicer-pipeline run this batch was dispatched as.
--
-- queue_item_id alone could not answer "has this bed been sent?". Slicing on
-- BambuBuddy is asynchronous: running a pipeline returns 202 Accepted with the
-- run still in progress, so there is no queue entry for seconds or minutes
-- afterwards. A dispatcher checking only queue_item_id would find it null on the
-- next pass and send the same plate again - and a batch is one physical print.
--
-- Recorded the moment the run is accepted, so the guard holds from the first
-- instant. queue_item_id is still filled in later, when BambuBuddy has queued
-- the sliced file; the two answer different questions and both are worth having.
ALTER TABLE batches ADD COLUMN IF NOT EXISTS pipeline_run_id integer;

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS pipeline_run_id;
