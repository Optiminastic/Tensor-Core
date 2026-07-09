"""Default assumptions, pricing ladders and thresholds (spec §4, §8, §9)."""

from app.pricing.models import Brand, CostAssumptions, MarginAssumptions

DEFAULT_COST_ASSUMPTIONS = CostAssumptions()
DEFAULT_MARGINS = MarginAssumptions()

# Launch stress test: re-run economics at a higher ad spend (spec §9).
STRESS_AD_SPEND_PCT = 0.40

# Approved selling-price ladders (spec §8).
GIFTING_LADDER: list[int] = [999, 1199, 1299, 1499, 1799, 1999, 2499, 2999, 3499, 3999]
DECOR_LADDER: list[int] = [3999, 4999, 5999, 6999, 7999, 9999, 12999, 14999, 19999, 24999]

# Design CP as a share of selling price — (green_max, yellow_max) per brand (spec §4).
CP_THRESHOLDS: dict[Brand, tuple[float, float]] = {
    Brand.GIFTING: (0.25, 0.30),
    Brand.DECOR: (0.30, 0.33),
}

# Target effective machine time per unit for entry-tier gifting (spec §4).
GIFTING_ENTRY_MACHINE_HOURS = 2.0


def ladder_for(brand: Brand) -> list[int]:
    return GIFTING_LADDER if brand == Brand.GIFTING else DECOR_LADDER
