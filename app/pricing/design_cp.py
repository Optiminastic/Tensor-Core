"""Design CP calculation (spec §2–§3)."""

from app.pricing.models import CostAssumptions, DesignCPBreakdown, SlicerMetrics


def compute_effective_machine_time(batch_print_time_hr: float, units_per_bed: int) -> float:
    """Per-unit machine time once a plate is batched (spec §2).

    Example: a 4-unit plate printing in 5.5h → 1.375h per unit.
    """
    if units_per_bed < 1:
        raise ValueError("units_per_bed must be >= 1")
    if batch_print_time_hr <= 0:
        raise ValueError("batch_print_time_hr must be > 0")
    return round(batch_print_time_hr / units_per_bed, 4)


def compute_design_cp(
    metrics: SlicerMetrics, assumptions: CostAssumptions
) -> DesignCPBreakdown:
    """Total Design CP for a single unit (spec §3)."""
    filament = metrics.filament_g / 1000 * assumptions.filament_cost_per_kg
    purge = metrics.purge_g / 1000 * assumptions.filament_cost_per_kg
    electricity = metrics.electricity_kwh * assumptions.electricity_cost_per_unit
    machine = metrics.effective_machine_time_hr * assumptions.machine_hour_cost
    finishing = assumptions.finishing_labour
    consumables = assumptions.consumables

    subtotal = filament + purge + electricity + machine + finishing + consumables
    failure_provision = subtotal * assumptions.failure_pct
    design_cp = subtotal + failure_provision

    return DesignCPBreakdown(
        filament=round(filament, 2),
        purge=round(purge, 2),
        electricity=round(electricity, 2),
        machine=round(machine, 2),
        finishing=round(finishing, 2),
        consumables=round(consumables, 2),
        failure_provision=round(failure_provision, 2),
        subtotal=round(subtotal, 2),
        design_cp=round(design_cp, 2),
    )
