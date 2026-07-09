from app.pricing.models import Brand, DesignStatus, SlicerMetrics
from app.pricing.status import evaluate_status


def test_green_gifting() -> None:
    # CP ₹240 on ₹999 = 24% ≤ 25% → green (matches the style-guide sample).
    result = evaluate_status(240, 999, Brand.GIFTING)
    assert result.status == DesignStatus.GREEN
    assert result.cp_pct == 0.2402


def test_yellow_when_slightly_over() -> None:
    result = evaluate_status(280, 999, Brand.GIFTING)  # 28%
    assert result.status == DesignStatus.YELLOW


def test_red_when_expensive() -> None:
    result = evaluate_status(350, 999, Brand.GIFTING)  # 35%
    assert result.status == DesignStatus.RED


def test_decor_allows_up_to_30pct() -> None:
    result = evaluate_status(1150, 3999, Brand.DECOR)  # 28.8% ≤ 30%
    assert result.status == DesignStatus.GREEN


def test_machine_time_downgrades_entry_gifting() -> None:
    metrics = SlicerMetrics(
        print_time_hr=3, filament_g=50, effective_machine_time_hr=2.5, units_per_bed=1
    )
    result = evaluate_status(200, 999, Brand.GIFTING, metrics)  # 20% green, but 2.5h > 2h
    assert result.status == DesignStatus.YELLOW
    assert any("machine time" in reason.lower() for reason in result.reasons)
