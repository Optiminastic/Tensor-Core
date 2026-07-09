"""Pydantic models for the costing & pricing engine (spec §2–§9)."""

from enum import StrEnum

from pydantic import BaseModel, Field


class Brand(StrEnum):
    GIFTING = "gifting"
    DECOR = "decor"


class DesignStatus(StrEnum):
    GREEN = "green"
    YELLOW = "yellow"
    RED = "red"


class SlicerMetrics(BaseModel):
    """Per-unit metrics extracted from the slicer engine (spec §2)."""

    print_time_hr: float = Field(gt=0)
    filament_g: float = Field(ge=0)
    purge_g: float = Field(default=0.0, ge=0)
    support_g: float = Field(default=0.0, ge=0)
    colour_changes: int = Field(default=0, ge=0)
    electricity_kwh: float = Field(default=0.0, ge=0)
    units_per_bed: int = Field(default=1, ge=1)
    effective_machine_time_hr: float = Field(gt=0)
    failure_risk: float = Field(default=0.0, ge=0, le=1)


class CostAssumptions(BaseModel):
    """Admin-editable cost inputs (spec §3). Defaults are mid-range."""

    filament_cost_per_kg: float = 1000.0
    electricity_cost_per_unit: float = 12.0
    machine_hour_cost: float = 45.0
    finishing_labour: float = 30.0
    consumables: float = 15.0
    failure_pct: float = 0.06


class DesignCPBreakdown(BaseModel):
    filament: float
    purge: float
    electricity: float
    machine: float
    finishing: float
    consumables: float
    failure_provision: float
    subtotal: float
    design_cp: float


class MarginAssumptions(BaseModel):
    """Reverse unit-economics assumptions (spec §9). Normal case defaults."""

    ad_spend_pct: float = 0.30
    team_pct: float = 0.11
    overhead_pct: float = 0.05
    target_profit_pct: float = 0.15


class FixedVariableCosts(BaseModel):
    """Per-order fixed variable costs excluding Design CP (spec §9)."""

    packaging: float = 0.0
    shipping: float = 0.0
    rto_cod: float = 0.0
    payment_gateway: float = 0.0
    tech_allocation: float = 0.0
    other: float = 0.0

    def total_excluding_cp(self) -> float:
        return round(
            self.packaging
            + self.shipping
            + self.rto_cod
            + self.payment_gateway
            + self.tech_allocation
            + self.other,
            2,
        )


class SellingPriceResult(BaseModel):
    raw_sp: float
    recommended_sp: int | None
    cp_pct_at_recommended: float | None
    passes_normal: bool
    survives_stress: bool
    warnings: list[str]


class DesignStatusResult(BaseModel):
    status: DesignStatus
    cp_pct: float
    reasons: list[str]
