"""Request/response schemas for the Projects API."""

import uuid
from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field

from app.db.models.projects import ProjectStatus
from app.pricing.models import Brand


class _ORMModel(BaseModel):
    model_config = ConfigDict(from_attributes=True)


class ProjectCreate(BaseModel):
    name: str = Field(min_length=1, max_length=120)
    brand: Brand
    description: str | None = Field(default=None, max_length=500)


class ProjectUpdate(BaseModel):
    name: str | None = Field(default=None, min_length=1, max_length=120)
    brand: Brand | None = None
    description: str | None = Field(default=None, max_length=500)
    status: ProjectStatus | None = None


class ProjectRead(_ORMModel):
    id: uuid.UUID
    name: str
    brand: Brand
    description: str | None
    status: ProjectStatus
    created_by: str
    created_at: datetime
    updated_at: datetime
