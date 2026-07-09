"""Green / Yellow / Red pre-check status (spec §4–§5)."""

from app.pricing.assumptions import CP_THRESHOLDS, GIFTING_ENTRY_MACHINE_HOURS
from app.pricing.models import Brand, DesignStatus, DesignStatusResult, SlicerMetrics


def evaluate_status(
    design_cp: float,
    target_sp: float,
    brand: Brand,
    metrics: SlicerMetrics | None = None,
) -> DesignStatusResult:
    """Classify a design against the CP-share and machine-time rules."""
    if target_sp <= 0:
        raise ValueError("target_sp must be positive")

    green_max, yellow_max = CP_THRESHOLDS[brand]
    cp_pct = round(design_cp / target_sp, 4)
    reasons: list[str] = []

    if cp_pct <= green_max:
        status = DesignStatus.GREEN
    elif cp_pct <= yellow_max:
        status = DesignStatus.YELLOW
        reasons.append(f"Design CP is {cp_pct:.0%} of price — above the {green_max:.0%} target.")
    else:
        status = DesignStatus.RED
        reasons.append(f"Design CP is {cp_pct:.0%} of price — over the {yellow_max:.0%} ceiling.")

    # Entry-tier gifting must batch under the machine-time target (spec §4).
    if (
        brand == Brand.GIFTING
        and target_sp <= 999
        and metrics is not None
        and metrics.effective_machine_time_hr > GIFTING_ENTRY_MACHINE_HOURS
    ):
        reasons.append(
            f"Effective machine time {metrics.effective_machine_time_hr:.2f}h exceeds the "
            f"{GIFTING_ENTRY_MACHINE_HOURS:.0f}h target for ₹999."
        )
        if status == DesignStatus.GREEN:
            status = DesignStatus.YELLOW

    return DesignStatusResult(status=status, cp_pct=cp_pct, reasons=reasons)
