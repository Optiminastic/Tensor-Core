from fastapi.testclient import TestClient

from app.auth.catalog import PRODUCTION_READ, PROJECT_MANAGE, PROJECT_READ
from tests.conftest import make_user

# The `client` fixture (an admin caller) and `make_client` come from conftest.

MISSING = "00000000-0000-0000-0000-000000000000"


def test_create_list_update_and_archive(client: TestClient) -> None:
    resp = client.post(
        "/projects",
        json={"name": "Diwali Gifting 2026", "brand": "gifting", "description": "Festival range"},
    )
    assert resp.status_code == 201
    body = resp.json()
    assert body["name"] == "Diwali Gifting 2026"
    assert body["brand"] == "gifting"
    assert body["status"] == "active"
    assert body["created_by"] == "usr_admin"  # from admin_user() in conftest
    project_id = body["id"]

    assert len(client.get("/projects").json()) == 1

    # Rename and archive.
    resp = client.patch(
        f"/projects/{project_id}", json={"name": "Diwali 2026", "status": "archived"}
    )
    assert resp.status_code == 200
    assert resp.json()["name"] == "Diwali 2026"
    assert resp.json()["status"] == "archived"


def test_name_is_required(client: TestClient) -> None:
    resp = client.post("/projects", json={"brand": "decor"})
    assert resp.status_code == 422


def test_get_missing_returns_404(client: TestClient) -> None:
    assert client.get(f"/projects/{MISSING}").status_code == 404


# ── Authorization ────────────────────────────────────────────────────


def test_anonymous_is_rejected(make_client) -> None:
    assert make_client(None).get("/projects").status_code == 401


def test_unrelated_permission_is_forbidden(make_client) -> None:
    operator = make_client(make_user(PRODUCTION_READ))
    assert operator.get("/projects").status_code == 403
    assert operator.post("/projects", json={"name": "X", "brand": "gifting"}).status_code == 403


def test_read_does_not_grant_write(make_client) -> None:
    reader = make_client(make_user(PROJECT_READ))
    assert reader.get("/projects").status_code == 200
    resp = reader.post("/projects", json={"name": "X", "brand": "gifting"})
    assert resp.status_code == 403


def test_manage_grants_write(make_client) -> None:
    writer = make_client(make_user(PROJECT_READ, PROJECT_MANAGE))
    resp = writer.post("/projects", json={"name": "Wall Art", "brand": "decor"})
    assert resp.status_code == 201


def test_missing_404s_only_after_permission_passes(make_client) -> None:
    # Order matters: 404-before-403 would let anyone probe which ids exist.
    unauthorized = make_client(make_user(PRODUCTION_READ))
    assert unauthorized.get(f"/projects/{MISSING}").status_code == 403

    authorized = make_client(make_user(PROJECT_READ))
    assert authorized.get(f"/projects/{MISSING}").status_code == 404
