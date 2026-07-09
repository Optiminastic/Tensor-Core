"""FastAPI router exposing the costing & pricing engine."""

from fastapi import APIRouter
from pydantic import BaseModel, Field

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
def selling_price_endpoint(req: SellingPriceRequest) -> SellingPriceResult:
    return generate_selling_price(req.design_cp, req.fixed_costs, req.brand, req.margins)


class StatusRequest(BaseModel):
    design_cp: float
    target_sp: float
    brand: Brand
    metrics: SlicerMetrics | None = None


@router.post("/status", response_model=DesignStatusResult)
def status_endpoint(req: StatusRequest) -> DesignStatusResult:
    return evaluate_status(req.design_cp, req.target_sp, req.brand, req.metrics)
