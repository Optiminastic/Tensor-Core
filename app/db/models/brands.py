"""Brand profiles — the editable identity and pricing policy per brand.

Tensor has exactly two brands (`gifting`, `decor`), fixed by the `Brand` enum
that is woven through the engine, config, and projects. This table does not add
new brands; it holds the **editable detail** for those two — their identity and,
crucially, the price ladder and CP thresholds the pricing engine used to hardcode
in `assumptions.py`.

Moving that policy here is what lets an admin "manage pricing ladders" (spec
§14). The engine stays pure: the API layer loads a row and passes plain values
into the pure functions; `assumptions.py` remains the built-in fallback and the
seed source.
"""

from sqlalchemy import JSON, Boolean, Integer, Numeric, String
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base, TimestampMixin, UUIDPk
from app.db.models.config import brand_enum
from app.pricing.models import Brand


class BrandProfile(UUIDPk, TimestampMixin, Base):
    """Editable identity + pricing policy for one brand."""

    __tablename__ = "brands"

    # --- Identity ---
    # One row per Brand enum value. Unique, so the enum key is the natural id.
    key: Mapped[Brand] = mapped_column(brand_enum, unique=True)
    name: Mapped[str] = mapped_column(String(120))
    starting_price: Mapped[float] = mapped_column(Numeric(10, 2, asdecimal=False))
    shopify_url: Mapped[str | None] = mapped_column(String(255), nullable=True)
    description: Mapped[str | None] = mapped_column(String(500), nullable=True)
    is_active: Mapped[bool] = mapped_column(Boolean, default=True)

    # --- Pricing policy (read by the engine) ---
    # The approved selling-price ladder, ascending. JSON rather than a Postgres
    # ARRAY so the same model works on the SQLite test database.
    ladder: Mapped[list[int]] = mapped_column(JSON)
    # Design CP as a share of selling price — green/yellow ceilings.
    cp_green_max: Mapped[float] = mapped_column(Numeric(5, 4, asdecimal=False))
    cp_yellow_max: Mapped[float] = mapped_column(Numeric(5, 4, asdecimal=False))
    # Entry-tier machine-time rule (gifting only today): the per-unit target and
    # the rung it applies at. Null when a brand has no such rule.
    entry_machine_hours: Mapped[float | None] = mapped_column(
        Numeric(5, 2, asdecimal=False), nullable=True
    )
    entry_rung: Mapped[int | None] = mapped_column(Integer, nullable=True)
