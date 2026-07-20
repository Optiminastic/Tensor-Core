"""FastAPI router exposing the costing & pricing engine."""

from typing import Annotated

from fastapi import APIRouter, Depends
from pydantic import BaseModel, Field
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.pricing.brand_policy import load_brand_policy
from app.pricing.design_cp import compute_design_cp
from app.pricing.models import (
    Brand,
    CostAssumptions,
    DesignCPBreakdown,
    DesignStatusResult,
    FixedVariableCosts,
    MarginAssumptions,
    SellingPriceResult,
    SlicerMetrics,
)
from app.pricing.selling_price import generate_selling_price
from app.pricing.status import evaluate_status

router = APIRouter(prefix="/pricing", tags=["pricing"])


class DesignCPRequest(BaseModel):
    metrics: SlicerMetrics
    assumptions: CostAssumptions = Field(default_factory=CostAssumptions)


@router.post("/design-cp", response_model=DesignCPBreakdown)
def design_cp_endpoint(req: DesignCPRequest) -> DesignCPBreakdown:
    return compute_design_cp(req.metrics, req.assumptions)


class SellingPriceRequest(BaseModel):
    design_cp: float
    brand: Brand
    fixed_costs: FixedVariableCosts = Field(default_factory=FixedVariableCosts)
    margins: MarginAssumptions = Field(default_factory=MarginAssumptions)


@router.post("/selling-price", response_model=SellingPriceResult)
def selling_price_endpoint(
    req: SellingPriceRequest, db: Annotated[Session, Depends(get_db)]
) -> SellingPriceResult:
    # Load the brand's editable ladder; the engine falls back to its built-in
    # policy if the brand has no row yet.
    policy = load_brand_policy(db, req.brand)
    return generate_selling_price(
        req.design_cp,
        req.fixed_costs,
        req.brand,
        req.margins,
        ladder=policy.ladder if policy else None,
    )


class StatusRequest(BaseModel):
    design_cp: float
    target_sp: float
    brand: Brand
    metrics: SlicerMetrics | None = None


@router.post("/status", response_model=DesignStatusResult)
def status_endpoint(
    req: StatusRequest, db: Annotated[Session, Depends(get_db)]
) -> DesignStatusResult:
    policy = load_brand_policy(db, req.brand)
    return evaluate_status(
        req.design_cp,
        req.target_sp,
        req.brand,
        req.metrics,
        thresholds=policy.thresholds if policy else None,
        entry_machine_hours=policy.entry_machine_hours if policy else None,
        entry_rung=policy.entry_rung if policy and policy.entry_rung is not None else 999,
    )
