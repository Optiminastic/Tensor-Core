-- +goose Up
-- The printer class a bed was laid out for.
--
-- Not a preference, and not the same thing as machine_id. A plate is packed at
-- fixed offsets and the slicer is told not to rearrange it (AutoOrient and
-- AutoArrange are both false), so the bed it was laid out on is the only bed it
-- can print on. Four planks packed on the A2L's 330x320 come out 270x270, and
-- the five P2S machines here are 256x256.
--
-- That is not hypothetical: every plate was packed on one hardcoded 330x320
-- bed whatever machine it went to, BATCH-1002076 was sent to P4, and BambuBuddy
-- answered "G-code conflicts detected after slicing ... try moving the wipe
-- tower further from other models" - there was nowhere left for the tower.
--
-- machine_id cannot carry this. It points at machine_profiles, and a class can
-- have more than one profile (this floor has two H2C profiles), so writing one
-- would pin a bed to a slicing configuration nobody chose. The family is the
-- bed's own property: what it was packed for.
ALTER TABLE batches
    ADD COLUMN IF NOT EXISTS machine_family varchar(16);

-- NULL means a bed planned before this existed, and those were all packed on
-- the 330x320 default - which is the A2L's bed. Reading NULL as "any machine"
-- would send exactly those beds to a P2S, which is the failure above. They are
-- backfilled rather than left to be interpreted.
UPDATE batches
SET machine_family = 'A2L'
WHERE machine_family IS NULL
  AND status IN ('pending_approval', 'open');

-- +goose Down
ALTER TABLE batches DROP COLUMN IF EXISTS machine_family;
