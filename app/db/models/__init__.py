"""Import all models here so Alembic's autogenerate can see them."""

from app.db.models.authz import (
    Permission,
    Role,
    RoleName,
    RolePermission,
    UserAuthzState,
    UserInvite,
    UserRole,
)
from app.db.models.brands import BrandProfile
from app.db.models.config import CostAssumptionSet, MachineProfile, MaterialProfile
from app.db.models.projects import Project, ProjectStatus

__all__ = [
    "BrandProfile",
    "CostAssumptionSet",
    "MachineProfile",
    "MaterialProfile",
    "Permission",
    "Project",
    "ProjectStatus",
    "Role",
    "RoleName",
    "RolePermission",
    "UserAuthzState",
    "UserInvite",
    "UserRole",
]
