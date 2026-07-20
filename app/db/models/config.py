"""Admin-editable configuration tables — the engine's inputs (spec §3, §14)."""

from sqlalchemy import Boolean, Enum, Numeric, String
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base, TimestampMixin, UUIDPk
from app.pricing.models import Brand

# The single SQLAlchemy Enum bound to the `brand` Postgres type. Shared so every
# table that stores a brand references one Enum object — two Enum(name="brand")
# instances in the metadata would clash on create_all and try to re-create the
# type in migrations. Imported by app/db/models/projects.py.
brand_enum = Enum(Brand, name="brand", values_callable=lambda e: [m.value for m in e])


class MaterialProfile(UUIDPk, TimestampMixin, Base):
    """A filament/material the farm stocks (spec §2 material type)."""

    __tablename__ = "material_profiles"

    name: Mapped[str] = mapped_column(String(120), unique=True)
    material_type: Mapped[str] = mapped_column(String(40))  # PLA, PETG, …
    cost_per_kg: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False))
    colour: Mapped[str | None] = mapped_column(String(60), nullable=True)
    is_active: Mapped[bool] = mapped_column(Boolean, default=True)


class MachineProfile(UUIDPk, TimestampMixin, Base):
    """A printer profile and its costed machine-hour rate (spec §1 machines)."""

    __tablename__ = "machine_profiles"

    name: Mapped[str] = mapped_column(String(120), unique=True)  # e.g. "Bambu H2C"
    machine_hour_cost: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False))
    is_active: Mapped[bool] = mapped_column(Boolean, default=True)


class CostAssumptionSet(UUIDPk, TimestampMixin, Base):
    """A named set of cost assumptions the engine consumes (spec §3)."""

    __tablename__ = "cost_assumption_sets"

    name: Mapped[str] = mapped_column(String(120), unique=True)
    brand: Mapped[Brand | None] = mapped_column(brand_enum, nullable=True)
    filament_cost_per_kg: Mapped[float] = mapped_column(
        Numeric(10, 2, asdecimal=False), default=1000
    )
    electricity_cost_per_unit: Mapped[float] = mapped_column(
        Numeric(10, 2, asdecimal=False), default=12
    )
    machine_hour_cost: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False), default=45)
    finishing_labour: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False), default=30)
    consumables: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False), default=15)
    failure_pct: Mapped[float] = mapped_column(Numeric(5, 4, asdecimal=False), default=0.06)
    is_default: Mapped[bool] = mapped_column(Boolean, default=False)
