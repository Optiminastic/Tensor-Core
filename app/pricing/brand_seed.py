"""Seed the two brands from the built-in pricing policy.

Idempotent and safe to re-run: it upserts the `gifting` and `decor` rows from the
`assumptions.py` constants, so the database reproduces the spec until an admin
edits a value. The engine reads from these rows; `assumptions.py` stays the
fallback and the source of these defaults.

Run with:  uv run python -m app.pricing.brand_seed
"""

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.db.models.brands import BrandProfile
from app.pricing.assumptions import (
    CP_THRESHOLDS,
    DECOR_LADDER,
    GIFTING_ENTRY_MACHINE_HOURS,
    GIFTING_LADDER,
)
from app.pricing.models import Brand


def _defaults() -> dict[Brand, dict]:
    gifting_green, gifting_yellow = CP_THRESHOLDS[Brand.GIFTING]
    decor_green, decor_yellow = CP_THRESHOLDS[Brand.DECOR]
    return {
        Brand.GIFTING: {
            "name": "Personalised Gifting",
            "starting_price": 999,
            "shopify_url": None,
            "description": "Personalised 3D-printed gifting. Starts at ₹999.",
            "ladder": GIFTING_LADDER,
            "cp_green_max": gifting_green,
            "cp_yellow_max": gifting_yellow,
            "entry_machine_hours": GIFTING_ENTRY_MACHINE_HOURS,
            "entry_rung": 999,
        },
        Brand.DECOR: {
            "name": "Decor",
            "starting_price": 3999,
            "shopify_url": None,
            "description": "Home, office and desk decor. Starts at ₹3,999.",
            "ladder": DECOR_LADDER,
            "cp_green_max": decor_green,
            "cp_yellow_max": decor_yellow,
            "entry_machine_hours": None,
            "entry_rung": None,
        },
    }


def sync_brands(db: Session) -> int:
    """Insert or update both brand rows from the defaults. Returns the count.

    Only fills a row that does not exist yet; it does not overwrite an admin's
    edits to an existing brand — the point of the table is that those edits win.
    """
    existing = {b.key: b for b in db.scalars(select(BrandProfile))}
    defaults = _defaults()

    for key, values in defaults.items():
        if key not in existing:
            db.add(BrandProfile(key=key, **values))

    db.flush()
    return len(defaults)


def main() -> None:
    from app.db.session import _ensure_initialised

    with _ensure_initialised()() as db:
        count = sync_brands(db)
        db.commit()
    print(f"Synced {count} brands.")


if __name__ == "__main__":
    main()
