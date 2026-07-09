"""Import all models here so Alembic's autogenerate can see them."""

from app.db.models.config import CostAssumptionSet, MachineProfile, MaterialProfile

__all__ = ["CostAssumptionSet", "MachineProfile", "MaterialProfile"]
