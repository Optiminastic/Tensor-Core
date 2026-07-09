from app.pricing.assumptions import DEFAULT_COST_ASSUMPTIONS
from app.pricing.design_cp import compute_design_cp, compute_effective_machine_time
from app.pricing.models import SlicerMetrics


def test_effective_machine_time_batches() -> None:
    # spec §2 example: a 4-unit plate printing in 5.5h → 1.375h per unit.
    assert compute_effective_machine_time(5.5, 4) == 1.375


def test_design_cp_breakdown() -> None:
    metrics = SlicerMetrics(
        print_time_hr=2.0,
        filament_g=60,
        purge_g=8,
        electricity_kwh=0.5,
        units_per_bed=4,
        effective_machine_time_hr=1.4,
    )
    cp = compute_design_cp(metrics, DEFAULT_COST_ASSUMPTIONS)

    assert cp.filament == 60.0  # 60g @ ₹1000/kg
    assert cp.purge == 8.0  # 8g @ ₹1000/kg
    assert cp.electricity == 6.0  # 0.5 kWh @ ₹12
    assert cp.machine == 63.0  # 1.4h @ ₹45
    assert cp.finishing == 30.0
    assert cp.consumables == 15.0
    assert cp.subtotal == 182.0
    assert cp.failure_provision == 10.92  # 6% of subtotal
    assert cp.design_cp == 192.92
