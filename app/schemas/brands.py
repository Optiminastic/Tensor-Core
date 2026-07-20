"""Request/response schemas for the Brands API."""

import uuid
from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field, field_validator

from app.pricing.models import Brand


class _ORMModel(BaseModel):
    model_config = ConfigDict(from_attributes=True)


def _ascending_ladder(ladder: list[int]) -> list[int]:
    if not ladder:
        raise ValueError("The ladder needs at least one price.")
    if any(price <= 0 for price in ladder):
        raise ValueError("Ladder prices must be positive.")
    if ladder != sorted(ladder) or len(set(ladder)) != len(ladder):
        raise ValueError("Ladder prices must be strictly ascending.")
    return ladder


class BrandRead(_ORMModel):
    id: uuid.UUID
    key: Brand
    name: str
    starting_price: float
    shopify_url: str | None
    description: str | None
    is_active: bool
    ladder: list[int]
    cp_green_max: float
    cp_yellow_max: float
    entry_machine_hours: float | None
    entry_rung: int | None
    created_at: datetime
    updated_at: datetime


class BrandUpdate(BaseModel):
    """A partial edit. `key` is immutable — it ties the row to the Brand enum."""

    name: str | None = Field(default=None, min_length=1, max_length=120)
    starting_price: float | None = Field(default=None, gt=0)
    shopify_url: str | None = Field(default=None, max_length=255)
    description: str | None = Field(default=None, max_length=500)
    is_active: bool | None = None
    ladder: list[int] | None = None
    cp_green_max: float | None = Field(default=None, gt=0, le=1)
    cp_yellow_max: float | None = Field(default=None, gt=0, le=1)
    entry_machine_hours: float | None = Field(default=None, gt=0)
    entry_rung: int | None = Field(default=None, gt=0)

    @field_validator("ladder")
    @classmethod
    def _check_ladder(cls, value: list[int] | None) -> list[int] | None:
        return _ascending_ladder(value) if value is not None else value
