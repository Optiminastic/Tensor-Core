"""Tensor backend — FastAPI entrypoint."""

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from app.api.admin import router as admin_router
from app.api.brands import router as brands_router
from app.api.config import router as config_router
from app.api.internal import router as internal_router
from app.api.projects import router as projects_router
from app.core.config import settings
from app.pricing.api import router as pricing_router

app = FastAPI(title="Tensor API", version="0.1.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.cors_origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.include_router(pricing_router)
app.include_router(config_router)
app.include_router(admin_router)
app.include_router(projects_router)
app.include_router(brands_router)
app.include_router(internal_router)


@app.get("/health", tags=["meta"])
def health() -> dict[str, str]:
    return {"status": "ok", "environment": settings.environment}
