"""Load a brand's editable pricing policy from the database.

This is the one place that does DB I/O for pricing, so the engine can stay pure:
the API calls this, then passes the plain values into `generate_selling_price` /
`evaluate_status`. When a brand has no row yet (unseeded), it returns None and the
engine falls back to its built-in `assumptions.py` policy.
"""

from typing import NamedTuple

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.db.models.brands import BrandProfile
from app.pricing.models import Brand


class BrandPolicy(NamedTuple):
    ladder: list[int]
    thresholds: tuple[float, float]  # (green_max, yellow_max)
    entry_machine_hours: float | None
    entry_rung: int | None


def load_brand_policy(db: Session, brand: Brand) -> BrandPolicy | None:
    """The brand's ladder + thresholds, or None if it has no row yet."""
    row = db.scalar(select(BrandProfile).where(BrandProfile.key == brand))
    if row is None:
        return None
    return BrandPolicy(
        ladder=list(row.ladder),
        thresholds=(row.cp_green_max, row.cp_yellow_max),
        entry_machine_hours=row.entry_machine_hours,
        entry_rung=row.entry_rung,
    )
