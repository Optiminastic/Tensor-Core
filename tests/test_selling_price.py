from app.pricing.models import Brand, FixedVariableCosts, MarginAssumptions
from app.pricing.selling_price import (
    generate_selling_price,
    remaining_margin,
    snap_up_to_ladder,
)

# The spec §10 worked example uses team 12%, overhead 5%, profit 15%, ad 30%.
SPEC_MARGINS = MarginAssumptions(
    ad_spend_pct=0.30, team_pct=0.12, overhead_pct=0.05, target_profit_pct=0.15
)


def test_remaining_margin_spec_example() -> None:
    assert round(remaining_margin(SPEC_MARGINS), 2) == 0.38
    # Launch stress test at 40% ad spend.
    assert round(remaining_margin(SPEC_MARGINS, ad_spend_pct=0.40), 2) == 0.28


def test_snap_up_to_ladder() -> None:
    ladder = [999, 1199, 1299]
    assert snap_up_to_ladder(1078.9, ladder) == 1199
    assert snap_up_to_ladder(999, ladder) == 999
    assert snap_up_to_ladder(5000, ladder) is None


def test_generate_selling_price_spec_example() -> None:
    # spec §10: Design CP ₹240, packaging+shipping+RTO ₹130, PG/COD ₹40 → fixed ₹410.
    fixed = FixedVariableCosts(packaging=90, shipping=25, rto_cod=15, payment_gateway=40)
    result = generate_selling_price(240, fixed, Brand.GIFTING, SPEC_MARGINS)

    assert round(result.raw_sp) == 1079  # 410 / 0.38 ≈ 1078.95
    assert result.recommended_sp == 1199  # smallest ladder rung ≥ raw
    assert result.passes_normal is True

    # spec §10: "₹999 not recommended unless Design CP is reduced below ~₹210".
    warn_999 = next(w for w in result.warnings if "₹999" in w)
    assert "210" in warn_999
