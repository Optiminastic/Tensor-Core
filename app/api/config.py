"""Admin config CRUD — material/machine profiles and cost assumptions (spec §14)."""

import uuid

from fastapi import APIRouter, Depends, HTTPException, status
from pydantic import BaseModel
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import CONFIG_MANAGE, CONFIG_READ
from app.auth.dependencies import require_permission
from app.db.base import Base
from app.db.models.config import CostAssumptionSet, MachineProfile, MaterialProfile
from app.db.session import get_db
from app.schemas.config import (
    CostAssumptionSetCreate,
    CostAssumptionSetRead,
    CostAssumptionSetUpdate,
    MachineProfileCreate,
    MachineProfileRead,
    MaterialProfileCreate,
    MaterialProfileRead,
    MaterialProfileUpdate,
)

router = APIRouter(prefix="/config", tags=["config"])

# These endpoints expose cost assumptions — filament cost, machine-hour rate,
# failure provision. That is the farm's cost base, and the Operator role is
# explicitly not allowed to see it, so every route below names a permission.
_READ = Depends(require_permission(CONFIG_READ))
_MANAGE = Depends(require_permission(CONFIG_MANAGE))


def _get_or_404[M: Base](db: Session, model: type[M], obj_id: uuid.UUID) -> M:
    obj = db.get(model, obj_id)
    if obj is None:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND, detail=f"{model.__name__} not found"
        )
    return obj


def _apply(obj: Base, payload: BaseModel) -> None:
    for key, value in payload.model_dump(exclude_unset=True).items():
        setattr(obj, key, value)


# ── Material profiles ────────────────────────────────────────────────
@router.get("/materials", response_model=list[MaterialProfileRead], dependencies=[_READ])
def list_materials(db: Session = Depends(get_db)) -> list[MaterialProfile]:
    return list(db.scalars(select(MaterialProfile).order_by(MaterialProfile.name)))


@router.post(
    "/materials",
    response_model=MaterialProfileRead,
    status_code=status.HTTP_201_CREATED,
    dependencies=[_MANAGE],
)
def create_material(
    payload: MaterialProfileCreate, db: Session = Depends(get_db)
) -> MaterialProfile:
    obj = MaterialProfile(**payload.model_dump())
    db.add(obj)
    db.commit()
    db.refresh(obj)
    return obj


@router.get("/materials/{material_id}", response_model=MaterialProfileRead, dependencies=[_READ])
def get_material(material_id: uuid.UUID, db: Session = Depends(get_db)) -> MaterialProfile:
    return _get_or_404(db, MaterialProfile, material_id)


@router.patch(
    "/materials/{material_id}", response_model=MaterialProfileRead, dependencies=[_MANAGE]
)
def update_material(
    material_id: uuid.UUID, payload: MaterialProfileUpdate, db: Session = Depends(get_db)
) -> MaterialProfile:
    obj = _get_or_404(db, MaterialProfile, material_id)
    _apply(obj, payload)
    db.commit()
    db.refresh(obj)
    return obj


# ── Machine profiles ─────────────────────────────────────────────────
@router.get("/machines", response_model=list[MachineProfileRead], dependencies=[_READ])
def list_machines(db: Session = Depends(get_db)) -> list[MachineProfile]:
    return list(db.scalars(select(MachineProfile).order_by(MachineProfile.name)))


@router.post(
    "/machines",
    response_model=MachineProfileRead,
    status_code=status.HTTP_201_CREATED,
    dependencies=[_MANAGE],
)
def create_machine(payload: MachineProfileCreate, db: Session = Depends(get_db)) -> MachineProfile:
    obj = MachineProfile(**payload.model_dump())
    db.add(obj)
    db.commit()
    db.refresh(obj)
    return obj


# ── Cost-assumption sets ─────────────────────────────────────────────
@router.get("/cost-assumptions", response_model=list[CostAssumptionSetRead], dependencies=[_READ])
def list_cost_assumptions(db: Session = Depends(get_db)) -> list[CostAssumptionSet]:
    return list(db.scalars(select(CostAssumptionSet).order_by(CostAssumptionSet.name)))


@router.post(
    "/cost-assumptions",
    response_model=CostAssumptionSetRead,
    status_code=status.HTTP_201_CREATED,
    dependencies=[_MANAGE],
)
def create_cost_assumptions(
    payload: CostAssumptionSetCreate, db: Session = Depends(get_db)
) -> CostAssumptionSet:
    obj = CostAssumptionSet(**payload.model_dump())
    db.add(obj)
    db.commit()
    db.refresh(obj)
    return obj


@router.get(
    "/cost-assumptions/{set_id}", response_model=CostAssumptionSetRead, dependencies=[_READ]
)
def get_cost_assumptions(set_id: uuid.UUID, db: Session = Depends(get_db)) -> CostAssumptionSet:
    return _get_or_404(db, CostAssumptionSet, set_id)


@router.patch(
    "/cost-assumptions/{set_id}", response_model=CostAssumptionSetRead, dependencies=[_MANAGE]
)
def update_cost_assumptions(
    set_id: uuid.UUID, payload: CostAssumptionSetUpdate, db: Session = Depends(get_db)
) -> CostAssumptionSet:
    obj = _get_or_404(db, CostAssumptionSet, set_id)
    _apply(obj, payload)
    db.commit()
    db.refresh(obj)
    return obj
