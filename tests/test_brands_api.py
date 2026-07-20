from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.auth.catalog import BRAND_MANAGE, BRAND_READ, PRODUCTION_READ
from app.pricing.brand_seed import sync_brands
from tests.conftest import make_user


def _seed(db: Session) -> None:
    sync_brands(db)
    db.commit()


def test_list_and_get_brands(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    brands = client.get("/brands").json()
    assert {b["key"] for b in brands} == {"gifting", "decor"}

    gifting = client.get("/brands/gifting").json()
    assert gifting["ladder"][0] == 999
    assert gifting["cp_green_max"] == 0.25
    assert gifting["entry_machine_hours"] == 2.0


def test_edit_identity_and_ladder(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    resp = client.patch(
        "/brands/gifting",
        json={"name": "Gifting Renamed", "ladder": [999, 1299, 1999]},
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["name"] == "Gifting Renamed"
    assert body["ladder"] == [999, 1299, 1999]


def test_ladder_must_be_ascending(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    resp = client.patch("/brands/gifting", json={"ladder": [1999, 999]})
    assert resp.status_code == 422


def test_green_cannot_exceed_yellow(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    resp = client.patch("/brands/gifting", json={"cp_green_max": 0.40, "cp_yellow_max": 0.30})
    assert resp.status_code == 400


def test_unconfigured_brand_returns_404(client: TestClient) -> None:
    # Valid key, but the brand seed has not run in this fresh db.
    assert client.get("/brands/gifting").status_code == 404


def test_invalid_key_is_422(client: TestClient) -> None:
    assert client.get("/brands/nonsense").status_code == 422


# ── Authorization ────────────────────────────────────────────────────


def test_anonymous_is_rejected(make_client) -> None:
    assert make_client(None).get("/brands").status_code == 401


def test_unrelated_permission_is_forbidden(make_client, db_session: Session) -> None:
    _seed(db_session)
    operator = make_client(make_user(PRODUCTION_READ))
    assert operator.get("/brands").status_code == 403
    assert operator.patch("/brands/gifting", json={"name": "x"}).status_code == 403


def test_read_does_not_grant_write(make_client, db_session: Session) -> None:
    _seed(db_session)
    reader = make_client(make_user(BRAND_READ))
    assert reader.get("/brands").status_code == 200
    assert reader.patch("/brands/gifting", json={"name": "x"}).status_code == 403


def test_manage_grants_write(make_client, db_session: Session) -> None:
    _seed(db_session)
    writer = make_client(make_user(BRAND_READ, BRAND_MANAGE))
    assert writer.patch("/brands/gifting", json={"name": "x"}).status_code == 200


def test_permission_checked_before_existence(make_client) -> None:
    # Unseeded brand: a caller without brand:read gets 403, not 404 — the guard
    # runs before the row lookup so ids/config state can't be probed.
    assert make_client(make_user(PRODUCTION_READ)).get("/brands/gifting").status_code == 403
