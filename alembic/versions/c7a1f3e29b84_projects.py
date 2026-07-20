"""projects

A grouping layer on top of the design pipeline. A project is metadata an admin
creates to organise work for a brand; it grants no permissions and (in this
phase) holds no members.

`brand` reuses the existing `brand` Postgres enum (created by the initial config
migration), so this migration must NOT recreate that type — hence
`create_type=False` on the brand column. `project_status` is new and is created
here (and dropped on downgrade). `created_by` holds a Better Auth user id with
no foreign key, like user_invites.created_by.

Revision ID: c7a1f3e29b84
Revises: a753367d3132
Create Date: 2026-07-18

"""
from typing import Sequence, Union

import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

from alembic import op

# revision identifiers, used by Alembic.
revision: str = 'c7a1f3e29b84'
down_revision: Union[str, Sequence[str], None] = 'a753367d3132'
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

# `brand` already exists (from the initial config migration) — reference it,
# never recreate it. `project_status` is new and is created explicitly below.
# Both are declared create_type=False so op.create_table does not emit a
# CREATE TYPE for either.
brand = postgresql.ENUM('gifting', 'decor', name='brand', create_type=False)
project_status = postgresql.ENUM('active', 'archived', name='project_status', create_type=False)


def upgrade() -> None:
    """Upgrade schema."""
    project_status.create(op.get_bind(), checkfirst=True)
    op.create_table(
        'projects',
        sa.Column('name', sa.String(length=120), nullable=False),
        sa.Column('brand', brand, nullable=False),
        sa.Column('description', sa.String(length=500), nullable=True),
        sa.Column('status', project_status, nullable=False),
        sa.Column('created_by', sa.String(length=64), nullable=False),
        sa.Column('id', sa.Uuid(), nullable=False),
        sa.Column(
            'created_at', sa.DateTime(timezone=True), server_default=sa.text('now()'), nullable=False
        ),
        sa.Column(
            'updated_at', sa.DateTime(timezone=True), server_default=sa.text('now()'), nullable=False
        ),
        sa.PrimaryKeyConstraint('id'),
        sa.UniqueConstraint('name'),
    )


def downgrade() -> None:
    """Downgrade schema."""
    op.drop_table('projects')
    # Drop only the type this migration created (brand is shared — leave it).
    project_status.drop(op.get_bind(), checkfirst=True)
