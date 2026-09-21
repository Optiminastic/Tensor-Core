-- The slice Tensor asked BambuBuddy for, while it is still running.
--
-- Sending a bed to a CHOSEN printer happens in two moves, because BambuBuddy
-- has no single call for it. Tensor uploads the plate and asks for it to be
-- sliced with that printer's presets and that printer's actual spool colours;
-- the slice answers 202 and finishes minutes later; only then does a queue item
-- exist, and only a queue item can carry printer_id.
--
-- So between those two moments the bed is dispatched but has no queue item and
-- no pipeline run - and both of those are what the double-send guards read. A
-- second press, or the dispatcher's next pass, would upload and slice the same
-- plate again, and the bed would print twice.
--
-- pipeline_run_id could not be reused for this. It is fed to
-- ListRunsForPipeline, a different id space entirely, so storing a slice job id
-- there would make the pin worker look up a run that does not exist.
--
-- Cleared when the slice finishes (the queue item id takes over) or when it
-- fails (so the bed can be sent again).

-- +goose Up
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS bambu_slice_job_id integer;

-- +goose Down
ALTER TABLE batches
    DROP COLUMN IF EXISTS bambu_slice_job_id;
