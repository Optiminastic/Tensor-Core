"""Brands API — an admin edits brand identity and pricing policy.

There are exactly two brands, fixed by the `Brand` enum, so there is no create or
delete — only read and edit. Editing a brand's ladder or thresholds is what the
pricing engine then reads, so these are the routes behind "manage pricing ladders"
(spec §14). Reads need `brand:read`, writes need `brand:manage`.
"""

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import BRAND_MANAGE, BRAND_READ
from app.auth.dependencies import require_permission
from app.db.models.brands import BrandProfile
from app.db.session import get_db
from app.pricing.models import Brand
from app.schemas.brands import BrandRead, BrandUpdate

router = APIRouter(prefix="/brands", tags=["brands"])

_READ = Depends(require_permission(BRAND_READ))
_MANAGE = Depends(require_permission(BRAND_MANAGE))


def _get_or_404(db: Session, key: Brand) -> BrandProfile:
    obj = db.scalar(select(BrandProfile).where(BrandProfile.key == key))
    if obj is None:
        # A valid brand key with no row means the brand seed has not run.
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Brand '{key.value}' is not configured yet.",
        )
    return obj


@router.get("", response_model=list[BrandRead], dependencies=[_READ])
def list_brands(db: Annotated[Session, Depends(get_db)]) -> list[BrandProfile]:
    return list(db.scalars(select(BrandProfile).order_by(BrandProfile.key)))


@router.get("/{key}", response_model=BrandRead, dependencies=[_READ])
def get_brand(key: Brand, db: Annotated[Session, Depends(get_db)]) -> BrandProfile:
    return _get_or_404(db, key)


@router.patch("/{key}", response_model=BrandRead, dependencies=[_MANAGE])
def update_brand(
    key: Brand,
    payload: BrandUpdate,
    db: Annotated[Session, Depends(get_db)],
) -> BrandProfile:
    obj = _get_or_404(db, key)
    for field, value in payload.model_dump(exclude_unset=True).items():
        setattr(obj, field, value)

    # Green must not exceed yellow, or the status thresholds invert.
    if obj.cp_green_max > obj.cp_yellow_max:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="The green threshold must be at or below the yellow threshold.",
        )

    db.commit()
    db.refresh(obj)
    return obj
