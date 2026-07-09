from collections.abc import Generator

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from app.db.base import Base
from app.db.session import get_db
from app.main import app


@pytest.fixture
def client() -> Generator[TestClient, None, None]:
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )
    Base.metadata.create_all(engine)
    testing_session = sessionmaker(bind=engine, autoflush=False, expire_on_commit=False)

    def override_get_db() -> Generator[Session, None, None]:
        db = testing_session()
        try:
            yield db
        finally:
            db.close()

    app.dependency_overrides[get_db] = override_get_db
    with TestClient(app) as test_client:
        yield test_client
    app.dependency_overrides.clear()


def test_create_list_and_update_material(client: TestClient) -> None:
    resp = client.post(
        "/config/materials",
        json={"name": "PLA White", "material_type": "PLA", "cost_per_kg": 1000},
    )
    assert resp.status_code == 201
    body = resp.json()
    assert body["name"] == "PLA White"
    assert body["is_active"] is True
    material_id = body["id"]

    assert len(client.get("/config/materials").json()) == 1

    resp = client.patch(f"/config/materials/{material_id}", json={"cost_per_kg": 1100})
    assert resp.status_code == 200
    assert resp.json()["cost_per_kg"] == 1100.0


def test_get_missing_material_returns_404(client: TestClient) -> None:
    resp = client.get("/config/materials/00000000-0000-0000-0000-000000000000")
    assert resp.status_code == 404


def test_create_cost_assumptions_uses_defaults(client: TestClient) -> None:
    resp = client.post(
        "/config/cost-assumptions", json={"name": "Gifting default", "brand": "gifting"}
    )
    assert resp.status_code == 201
    body = resp.json()
    assert body["failure_pct"] == 0.06
    assert body["machine_hour_cost"] == 45.0
    assert body["brand"] == "gifting"


def test_create_machine(client: TestClient) -> None:
    resp = client.post("/config/machines", json={"name": "Bambu H2C", "machine_hour_cost": 45})
    assert resp.status_code == 201
    assert resp.json()["name"] == "Bambu H2C"
