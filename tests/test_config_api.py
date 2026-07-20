from fastapi.testclient import TestClient

from app.auth.catalog import CONFIG_MANAGE, CONFIG_READ, PRODUCTION_READ
from tests.conftest import make_user

# The `client` fixture (an admin caller) and `make_client` come from conftest.


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


# ── Authorization ────────────────────────────────────────────────────
# Cost assumptions are the farm's cost base. These assert the guards actually
# refuse, which is the half that matters.


def test_anonymous_caller_is_rejected(make_client) -> None:
    resp = make_client(None).get("/config/materials")
    assert resp.status_code == 401


def test_operator_cannot_read_cost_assumptions(make_client) -> None:
    """The Operator role runs the floor and must never see costs."""
    operator = make_client(make_user(PRODUCTION_READ))
    assert operator.get("/config/cost-assumptions").status_code == 403
    assert operator.get("/config/materials").status_code == 403


def test_read_permission_does_not_grant_write(make_client) -> None:
    """config:read is not config:manage. Reading costs must not imply editing them."""
    reader = make_client(make_user(CONFIG_READ))

    assert reader.get("/config/materials").status_code == 200
    resp = reader.post(
        "/config/materials",
        json={"name": "PLA Black", "material_type": "PLA", "cost_per_kg": 1000},
    )
    assert resp.status_code == 403


def test_manage_permission_grants_write(make_client) -> None:
    writer = make_client(make_user(CONFIG_READ, CONFIG_MANAGE))
    resp = writer.post("/config/machines", json={"name": "Bambu X1C", "machine_hour_cost": 50})
    assert resp.status_code == 201


def test_missing_material_404s_only_after_permission_passes(make_client) -> None:
    """A caller without permission gets 403, not 404.

    Order matters: 404-before-403 would let anyone probe which ids exist.
    """
    unauthorized = make_client(make_user(PRODUCTION_READ))
    missing = "00000000-0000-0000-0000-000000000000"
    assert unauthorized.get(f"/config/materials/{missing}").status_code == 403

    authorized = make_client(make_user(CONFIG_READ))
    assert authorized.get(f"/config/materials/{missing}").status_code == 404
