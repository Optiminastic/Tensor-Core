"""Projects — a grouping layer on top of the design pipeline.

A project is a named container an admin creates to organise work for a brand.
It is a deliberate extension beyond the costing/pricing spec, which has no
project entity: the spec is design-centric with global roles.

In this phase a project is metadata only. Membership (who works on a project)
and links to designs come later; crucially, a project does NOT grant
permissions — people keep their single global role, and a project only groups
work. So there is nothing here the auth layer needs to know about.
"""

from enum import StrEnum

from sqlalchemy import Enum, String
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base, TimestampMixin, UUIDPk
from app.db.models.config import brand_enum
from app.pricing.models import Brand


class ProjectStatus(StrEnum):
    """Lifecycle of a project. Archived projects are hidden, not deleted, so any
    designs later attached to them keep their history."""

    ACTIVE = "active"
    ARCHIVED = "archived"


_status_enum = Enum(
    ProjectStatus, name="project_status", values_callable=lambda e: [m.value for m in e]
)


class Project(UUIDPk, TimestampMixin, Base):
    """A named unit of work for one brand."""

    __tablename__ = "projects"

    name: Mapped[str] = mapped_column(String(120), unique=True)
    brand: Mapped[Brand] = mapped_column(brand_enum)
    description: Mapped[str | None] = mapped_column(String(500), nullable=True)
    status: Mapped[ProjectStatus] = mapped_column(_status_enum, default=ProjectStatus.ACTIVE)
    # The admin who created it — a Better Auth user id. No FK: that table belongs
    # to Better Auth's CLI, not Alembic (mirrors UserInvite.created_by).
    created_by: Mapped[str] = mapped_column(String(64))
