-- +goose Up
-- What the customer actually asked for, on the job that has to make it.
--
-- Until now a job carried its two names mashed into one string
-- ("SABIYA & IMRAN") and nothing else. The heart count, the colour and the
-- light option lived only on the ORDER's line-item jsonb, so the person holding
-- the job could not see what they were building without opening the order - and
-- on a two-product order, working out which line was theirs.
--
-- A SNAPSHOT, not a lookup. The job is the unit of work: it must show what it
-- was created from even after the order is re-synced and its line items are
-- rewritten, the same reason material, colour and bbox are already copied here
-- rather than joined at read time.
ALTER TABLE production_jobs
    -- The option string as the storefront states it, e.g. "BABY PINK / NO
    -- LIGHT" - the colour and the light choice in one, because that is how the
    -- store sells it and splitting it would invent structure it does not have.
    ADD COLUMN IF NOT EXISTS variant_title varchar(255),
    -- Every custom attribute for this line, verbatim and in the order the
    -- customer answered: the numbered STEP questions plus the store's own
    -- bookkeeping. Kept whole because a mapping only knows the keys it was
    -- told about, and the operator needs the ones it was not.
    ADD COLUMN IF NOT EXISTS personalisation_properties jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE production_jobs
    DROP COLUMN IF EXISTS variant_title,
    DROP COLUMN IF EXISTS personalisation_properties;
