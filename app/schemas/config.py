"""Request/response schemas for the Admin config API (spec §14)."""

import uuid
from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field

from app.pricing.models import Brand


class _ORMModel(BaseModel):
    model_config = ConfigDict(from_attributes=True)


# ── Material profiles ────────────────────────────────────────────────
class MaterialProfileCreate(BaseModel):
    name: str = Field(min_length=1, max_length=120)
    material_type: str = Field(min_length=1, max_length=40)
    cost_per_kg: float = Field(gt=0)
    colour: str | None = None
    is_active: bool = True


class MaterialProfileUpdate(BaseModel):
    material_type: str | None = Field(default=None, max_length=40)
    cost_per_kg: float | None = Field(default=None, gt=0)
    colour: str | None = None
    is_active: bool | None = None


class MaterialProfileRead(_ORMModel):
    id: uuid.UUID
    name: str
    material_type: str
    cost_per_kg: float
    colour: str | None
    is_active: bool
    created_at: datetime
    updated_at: datetime


# ── Machine profiles ─────────────────────────────────────────────────
class MachineProfileCreate(BaseModel):
    name: str = Field(min_length=1, max_length=120)
    machine_hour_cost: float = Field(gt=0)
    is_active: bool = True


class MachineProfileRead(_ORMModel):
    id: uuid.UUID
    name: str
    machine_hour_cost: float
    is_active: bool
    created_at: datetime
    updated_at: datetime


# ── Cost-assumption sets ─────────────────────────────────────────────
class CostAssumptionSetCreate(BaseModel):
    name: str = Field(min_length=1, max_length=120)
    brand: Brand | None = None
    filament_cost_per_kg: float = Field(default=1000, gt=0)
    electricity_cost_per_unit: float = Field(default=12, ge=0)
    machine_hour_cost: float = Field(default=45, ge=0)
    finishing_labour: float = Field(default=30, ge=0)
    consumables: float = Field(default=15, ge=0)
    failure_pct: float = Field(default=0.06, ge=0, le=1)
    is_default: bool = False


class CostAssumptionSetUpdate(BaseModel):
    filament_cost_per_kg: float | None = Field(default=None, gt=0)
    electricity_cost_per_unit: float | None = Field(default=None, ge=0)
    machine_hour_cost: float | None = Field(default=None, ge=0)
    finishing_labour: float | None = Field(default=None, ge=0)
    consumables: float | None = Field(default=None, ge=0)
    failure_pct: float | None = Field(default=None, ge=0, le=1)
    is_default: bool | None = None


class CostAssumptionSetRead(_ORMModel):
    id: uuid.UUID
    name: str
    brand: Brand | None
    filament_cost_per_kg: float
    electricity_cost_per_unit: float
    machine_hour_cost: float
    finishing_labour: float
    consumables: float
    failure_pct: float
    is_default: bool
    created_at: datetime
    updated_at: datetime
