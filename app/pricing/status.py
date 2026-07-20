"""Green / Yellow / Red pre-check status (spec §4–§5)."""

from app.pricing.assumptions import CP_THRESHOLDS, GIFTING_ENTRY_MACHINE_HOURS
from app.pricing.models import Brand, DesignStatus, DesignStatusResult, SlicerMetrics


def evaluate_status(
    design_cp: float,
    target_sp: float,
    brand: Brand,
    metrics: SlicerMetrics | None = None,
    thresholds: tuple[float, float] | None = None,
    entry_machine_hours: float | None = None,
    entry_rung: int = 999,
) -> DesignStatusResult:
    """Classify a design against the CP-share and machine-time rules.

    `thresholds`, `entry_machine_hours` and `entry_rung` default to the brand's
    built-in policy so the engine works standalone; the API passes the
    admin-editable values loaded from the brand record.
    """
    if target_sp <= 0:
        raise ValueError("target_sp must be positive")

    if thresholds is None:
        thresholds = CP_THRESHOLDS[brand]
    green_max, yellow_max = thresholds
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

    # Entry-tier machine-time rule (spec §4): gifting must batch under the target.
    # When no explicit target is given, fall back to gifting's built-in policy so
    # standalone behaviour is unchanged; decor has no such rule.
    if entry_machine_hours is None and brand == Brand.GIFTING:
        entry_machine_hours = GIFTING_ENTRY_MACHINE_HOURS
        entry_rung = 999
    if (
        entry_machine_hours is not None
        and target_sp <= entry_rung
        and metrics is not None
        and metrics.effective_machine_time_hr > entry_machine_hours
    ):
        reasons.append(
            f"Effective machine time {metrics.effective_machine_time_hr:.2f}h exceeds the "
            f"{entry_machine_hours:.0f}h target for ₹{entry_rung}."
        )
        if status == DesignStatus.GREEN:
            status = DesignStatus.YELLOW

    return DesignStatusResult(status=status, cp_pct=cp_pct, reasons=reasons)
