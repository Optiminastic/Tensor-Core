-- Records what a generated model was actually built from.
--
-- A rendered plank's file is named for the job and the two names -
-- "JOB-115059-2-APRAJITA-AJAY.3mf" - and that is all there was. It is enough to
-- read and not enough to CHECK: the heart count never appears in it, and the
-- names cannot be parsed back out reliably because a customer's name may carry
-- the same hyphen the filename separates on.
--
-- That mattered the day a bed printed wrong. plankParamsForJob matched a job to
-- its order line by SKU, and every line of a five-plank order carries the same
-- SKU, so four planks were built from the first line: NAVYA & KRISHNA four
-- times, with the first line's 2 hearts, against orders for APRAJITA & AJAY (1
-- heart), FARAH & ABID and CHANDNI & MILAN. Nothing in the database disagreed
-- with itself - the model was simply built from the wrong input, and no stored
-- fact recorded which input that was.
--
-- So the render's own inputs are written beside the file: the template it used,
-- the heart count, and both names as given to OpenSCAD. "Is this model still
-- what the order says?" becomes an exact comparison rather than a guess at a
-- filename, which is what the batch rebuild pass needs to re-render only the
-- jobs that are actually wrong.
--
-- jsonb rather than four columns: these are one fact - the arguments of one
-- render - and they travel together. A template that later takes another
-- parameter extends the document instead of the table.
--
-- Nullable, and left null for everything already stored. A file rendered before
-- this migration has no record of its inputs and cannot be given one
-- retrospectively; the rebuild pass reads a missing document as "unknown", which
-- is the honest answer and puts the job in the re-render list rather than
-- silently passing it.

-- +goose Up
ALTER TABLE file_assets
    ADD COLUMN IF NOT EXISTS render_params jsonb;

-- +goose Down
ALTER TABLE file_assets
    DROP COLUMN IF EXISTS render_params;
