"""The DB-driven pricing path reproduces the spec, and edits reach the engine."""

from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.pricing.brand_policy import load_brand_policy
from app.pricing.brand_seed import sync_brands
from app.pricing.models import Brand

SPEC_BODY = {
    "design_cp": 240,
    "brand": "gifting",
    "fixed_costs": {"packaging": 90, "shipping": 25, "rto_cod": 15, "payment_gateway": 40},
    "margins": {
        "ad_spend_pct": 0.30,
        "team_pct": 0.12,
        "overhead_pct": 0.05,
        "target_profit_pct": 0.15,
    },
}


def _seed(db: Session) -> None:
    sync_brands(db)
    db.commit()


def test_loader_returns_the_seeded_policy(db_session: Session) -> None:
    _seed(db_session)
    policy = load_brand_policy(db_session, Brand.GIFTING)
    assert policy is not None
    assert policy.ladder[0] == 999
    assert policy.thresholds == (0.25, 0.30)
    assert policy.entry_machine_hours == 2.0
    assert policy.entry_rung == 999


def test_db_driven_selling_price_matches_spec(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    body = client.post("/pricing/selling-price", json=SPEC_BODY).json()
    # Same worked example as the spec (§10): raw ~1079 snaps up to 1199.
    assert body["recommended_sp"] == 1199


def test_db_driven_status_matches_spec(client: TestClient, db_session: Session) -> None:
    _seed(db_session)
    green = client.post(
        "/pricing/status", json={"design_cp": 240, "target_sp": 999, "brand": "gifting"}
    ).json()
    assert green["status"] == "green"

    # The ₹999 machine-time downgrade still fires from the DB policy.
    downgraded = client.post(
        "/pricing/status",
        json={
            "design_cp": 200,
            "target_sp": 999,
            "brand": "gifting",
            "metrics": {
                "print_time_hr": 2.5,
                "filament_g": 60,
                "effective_machine_time_hr": 2.5,
                "units_per_bed": 1,
            },
        },
    ).json()
    assert downgraded["status"] == "yellow"
    assert any("machine time" in r for r in downgraded["reasons"])


def test_editing_the_ladder_changes_the_recommendation(
    client: TestClient, db_session: Session
) -> None:
    _seed(db_session)
    # Baseline: 1199 is available.
    assert client.post("/pricing/selling-price", json=SPEC_BODY).json()["recommended_sp"] == 1199

    # Drop the 1199 rung; the recommendation must climb to the next rung.
    client.patch("/brands/gifting", json={"ladder": [999, 1299, 1499, 1999, 2499, 2999]})
    after = client.post("/pricing/selling-price", json=SPEC_BODY).json()
    assert after["recommended_sp"] == 1299
