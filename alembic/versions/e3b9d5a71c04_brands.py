"""brands

Editable identity + pricing policy per brand. The two brands are fixed by the
`Brand` enum; this table holds their editable detail, including the ladder and CP
thresholds the engine used to hardcode.

`key` reuses the existing `brand` Postgres enum (create_type=False). `ladder` is
JSON (a list of ints) so the model also works on the SQLite test database.

Revision ID: e3b9d5a71c04
Revises: c7a1f3e29b84
Create Date: 2026-07-18

"""
from typing import Sequence, Union

import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

from alembic import op

# revision identifiers, used by Alembic.
revision: str = 'e3b9d5a71c04'
down_revision: Union[str, Sequence[str], None] = 'c7a1f3e29b84'
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

# Reuse the existing `brand` type — do not recreate it.
brand = postgresql.ENUM('gifting', 'decor', name='brand', create_type=False)


def upgrade() -> None:
    """Upgrade schema."""
    op.create_table(
        'brands',
        sa.Column('key', brand, nullable=False),
        sa.Column('name', sa.String(length=120), nullable=False),
        sa.Column('starting_price', sa.Numeric(precision=10, scale=2, asdecimal=False), nullable=False),
        sa.Column('shopify_url', sa.String(length=255), nullable=True),
        sa.Column('description', sa.String(length=500), nullable=True),
        sa.Column('is_active', sa.Boolean(), nullable=False),
        sa.Column('ladder', sa.JSON(), nullable=False),
        sa.Column('cp_green_max', sa.Numeric(precision=5, scale=4, asdecimal=False), nullable=False),
        sa.Column('cp_yellow_max', sa.Numeric(precision=5, scale=4, asdecimal=False), nullable=False),
        sa.Column('entry_machine_hours', sa.Numeric(precision=5, scale=2, asdecimal=False), nullable=True),
        sa.Column('entry_rung', sa.Integer(), nullable=True),
        sa.Column('id', sa.Uuid(), nullable=False),
        sa.Column(
            'created_at', sa.DateTime(timezone=True), server_default=sa.text('now()'), nullable=False
        ),
        sa.Column(
            'updated_at', sa.DateTime(timezone=True), server_default=sa.text('now()'), nullable=False
        ),
        sa.PrimaryKeyConstraint('id'),
        sa.UniqueConstraint('key'),
    )


def downgrade() -> None:
    """Downgrade schema."""
    op.drop_table('brands')
