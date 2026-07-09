"""Selling-price generation via reverse unit economics (spec §8–§10)."""

from app.pricing.assumptions import DEFAULT_MARGINS, STRESS_AD_SPEND_PCT, ladder_for
from app.pricing.models import (
    Brand,
    FixedVariableCosts,
    MarginAssumptions,
    SellingPriceResult,
)


def remaining_margin(margins: MarginAssumptions, ad_spend_pct: float | None = None) -> float:
    """1 − ad − team − overhead − target profit (spec §9)."""
    ad = margins.ad_spend_pct if ad_spend_pct is None else ad_spend_pct
    return 1 - ad - margins.team_pct - margins.overhead_pct - margins.target_profit_pct


def snap_up_to_ladder(raw_sp: float, ladder: list[int]) -> int | None:
    """Smallest approved ladder price at or above the raw price (spec §8)."""
    for price in ladder:
        if price >= raw_sp:
            return price
    return None


def generate_selling_price(
    design_cp: float,
    fixed_costs: FixedVariableCosts,
    brand: Brand,
    margins: MarginAssumptions = DEFAULT_MARGINS,
) -> SellingPriceResult:
    """Recommend a ladder price that clears unit economics (spec §9–§10).

    SP = fixed variable costs / remaining margin, snapped up to the approved
    ladder. Also reports whether the price survives the 40% ad-spend stress
    test, and — for each cheaper rung it can't support — the max Design CP
    that *would* make that rung viable.
    """
    margin_normal = remaining_margin(margins)
    if margin_normal <= 0:
        raise ValueError("Remaining margin must be positive; check the margin assumptions.")

    other_fixed = fixed_costs.total_excluding_cp()
    fixed_variable = round(design_cp + other_fixed, 2)
    raw_sp = round(fixed_variable / margin_normal, 2)

    ladder = ladder_for(brand)
    recommended = snap_up_to_ladder(raw_sp, ladder)
    cp_pct_at_recommended = round(design_cp / recommended, 4) if recommended else None

    margin_stress = remaining_margin(margins, ad_spend_pct=STRESS_AD_SPEND_PCT)
    survives_stress = bool(
        recommended and margin_stress > 0 and recommended >= fixed_variable / margin_stress
    )

    warnings = _rung_warnings(design_cp, other_fixed, margin_normal, ladder, recommended)
    if recommended is None:
        warnings.append("Raw price exceeds the top of the ladder — review costs or category.")

    return SellingPriceResult(
        raw_sp=raw_sp,
        recommended_sp=recommended,
        cp_pct_at_recommended=cp_pct_at_recommended,
        passes_normal=recommended is not None,
        survives_stress=survives_stress,
        warnings=warnings,
    )


def _rung_warnings(
    design_cp: float,
    other_fixed: float,
    margin_normal: float,
    ladder: list[int],
    recommended: int | None,
) -> list[str]:
    """For each rung below the recommendation, the max Design CP that fits it."""
    warnings: list[str] = []
    for price in ladder:
        if recommended is not None and price >= recommended:
            break
        max_cp = round(price * margin_normal - other_fixed, 2)
        if max_cp >= 0:
            warnings.append(
                f"₹{price} needs Design CP ≤ ₹{max_cp:.0f} (currently ₹{design_cp:.0f})."
            )
        else:
            warnings.append(f"₹{price} is not viable at these fixed costs.")
    return warnings
