-- +goose Up
-- Make the selling price fully data-driven. Two changes, both idempotent:
--
-- 1. cost_assumption_sets gains the two remaining pricing inputs the engine
--    needs but had no DB home: the per-unit fixed/variable costs (packaging,
--    shipping, RTO/COD, payment gateway, tech allocation, other) and the margin
--    assumptions (ad spend, team, overhead, target profit). Stored as json so a
--    row maps straight onto pricing.FixedVariableCosts / pricing.MarginAssumptions
--    (whose own defaults apply when the object is '{}').
--
-- 2. design_pricing keeps the full SellingPriceResult, not just the snapped
--    rung: the raw reverse-economics price, the CP share at the recommendation,
--    whether it passes at 30% ad spend and survives the 40% stress test, and the
--    per-rung warnings. These were computed then discarded.

ALTER TABLE cost_assumption_sets
    ADD COLUMN IF NOT EXISTS fixed_costs json NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS margins     json NOT NULL DEFAULT '{}';

ALTER TABLE design_pricing
    ADD COLUMN IF NOT EXISTS raw_sp                numeric(12, 2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cp_pct_at_recommended numeric(6, 4),
    ADD COLUMN IF NOT EXISTS passes_normal         boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS survives_stress       boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS sp_warnings           json NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE cost_assumption_sets
    DROP COLUMN IF EXISTS fixed_costs,
    DROP COLUMN IF EXISTS margins;

ALTER TABLE design_pricing
    DROP COLUMN IF EXISTS raw_sp,
    DROP COLUMN IF EXISTS cp_pct_at_recommended,
    DROP COLUMN IF EXISTS passes_normal,
    DROP COLUMN IF EXISTS survives_stress,
    DROP COLUMN IF EXISTS sp_warnings;
