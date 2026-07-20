"""Projects API — an admin groups work into named projects.

Projects are a grouping layer, not an access boundary: membership and roles are
unchanged by this router, so the only thing guarded here is managing the project
records themselves. Reads need `project:read`, writes need `project:manage`.
"""

import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, status
from pydantic import BaseModel
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import PROJECT_MANAGE, PROJECT_READ
from app.auth.dependencies import CurrentUser, require_permission
from app.db.base import Base
from app.db.models.projects import Project
from app.db.session import get_db
from app.schemas.projects import ProjectCreate, ProjectRead, ProjectUpdate

router = APIRouter(prefix="/projects", tags=["projects"])

# Managing projects is admin-only for now; the split mirrors /config and leaves
# room to grant read to other roles later without touching write.
_READ = Depends(require_permission(PROJECT_READ))
_MANAGE = Depends(require_permission(PROJECT_MANAGE))


def _get_or_404(db: Session, project_id: uuid.UUID) -> Project:
    obj = db.get(Project, project_id)
    if obj is None:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Project not found")
    return obj


def _apply(obj: Base, payload: BaseModel) -> None:
    for key, value in payload.model_dump(exclude_unset=True).items():
        setattr(obj, key, value)


@router.get("", response_model=list[ProjectRead], dependencies=[_READ])
def list_projects(db: Annotated[Session, Depends(get_db)]) -> list[Project]:
    return list(db.scalars(select(Project).order_by(Project.name)))


@router.post(
    "", response_model=ProjectRead, status_code=status.HTTP_201_CREATED, dependencies=[_MANAGE]
)
def create_project(
    payload: ProjectCreate,
    user: CurrentUser,
    db: Annotated[Session, Depends(get_db)],
) -> Project:
    # created_by comes from the verified token, never the request body.
    obj = Project(**payload.model_dump(), created_by=user.id)
    db.add(obj)
    db.commit()
    db.refresh(obj)
    return obj


@router.get("/{project_id}", response_model=ProjectRead, dependencies=[_READ])
def get_project(project_id: uuid.UUID, db: Annotated[Session, Depends(get_db)]) -> Project:
    return _get_or_404(db, project_id)


@router.patch("/{project_id}", response_model=ProjectRead, dependencies=[_MANAGE])
def update_project(
    project_id: uuid.UUID,
    payload: ProjectUpdate,
    db: Annotated[Session, Depends(get_db)],
) -> Project:
    obj = _get_or_404(db, project_id)
    _apply(obj, payload)
    db.commit()
    db.refresh(obj)
    return obj
