from fastapi.testclient import TestClient

from app.main import app

client = TestClient(app)


def test_health() -> None:
    response = client.get("/health")
    assert response.status_code == 200
    assert response.json()["status"] == "ok"


def test_selling_price_endpoint() -> None:
    response = client.post(
        "/pricing/selling-price",
        json={
            "design_cp": 240,
            "brand": "gifting",
            "fixed_costs": {"packaging": 90, "shipping": 25, "rto_cod": 15, "payment_gateway": 40},
            "margins": {
                "ad_spend_pct": 0.30,
                "team_pct": 0.12,
                "overhead_pct": 0.05,
                "target_profit_pct": 0.15,
            },
        },
    )
    assert response.status_code == 200
    body = response.json()
    assert body["recommended_sp"] == 1199
