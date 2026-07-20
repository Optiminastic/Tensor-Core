"""Roles, permissions and their assignment — the backend owns authorization.

Authentication (who you are) belongs to Better Auth in the frontend, which owns
the `user`, `session`, `account`, `verification` and `jwks` tables. This module
owns the separate question of what you may do.

The two halves meet in one place: `UserRole.user_id` holds a Better Auth user
id, and Better Auth reads these tables (over HTTP, via /internal/users/{id}/authz)
when it mints an access token.

Note the deliberate absence of a foreign key to `user`. That table belongs to a
different migration tool, so a database-level constraint here would couple
Alembic to Better Auth's CLI and force a fixed apply order. An orphaned row is
harmless: no user means no token, which means no authorization.
"""

import uuid
from datetime import datetime
from enum import StrEnum

from sqlalchemy import DateTime, ForeignKey, Index, Integer, String, UniqueConstraint
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.db.base import Base, TimestampMixin, UUIDPk


class RoleName(StrEnum):
    """The roles Tensor ships with.

    Adding a role must not require touching authentication. New values here plus
    a seeded row is the whole change — guards check permissions, never roles.
    """

    ADMIN = "ADMIN"
    DESIGNER = "DESIGNER"
    PROJECT_LEAD = "PROJECT_LEAD"
    PERFORMANCE_MARKETER = "PERFORMANCE_MARKETER"
    OPERATOR = "OPERATOR"


class Role(UUIDPk, TimestampMixin, Base):
    """A named bundle of permissions."""

    __tablename__ = "roles"

    name: Mapped[str] = mapped_column(String(40), unique=True)
    description: Mapped[str] = mapped_column(String(200))

    permissions: Mapped[list["RolePermission"]] = relationship(
        back_populates="role", cascade="all, delete-orphan"
    )
    users: Mapped[list["UserRole"]] = relationship(
        back_populates="role", cascade="all, delete-orphan"
    )


class Permission(UUIDPk, TimestampMixin, Base):
    """One `resource:action` capability, e.g. `pricing:override`.

    Guards name a permission, never a role, so that granting a new role access
    to an endpoint is a data change rather than a code change.
    """

    __tablename__ = "permissions"
    __table_args__ = (UniqueConstraint("resource", "action", name="uq_permission_resource_action"),)

    resource: Mapped[str] = mapped_column(String(40))
    action: Mapped[str] = mapped_column(String(40))
    description: Mapped[str] = mapped_column(String(200))

    roles: Mapped[list["RolePermission"]] = relationship(
        back_populates="permission", cascade="all, delete-orphan"
    )

    @property
    def key(self) -> str:
        """The wire form the token carries and guards compare against."""
        return f"{self.resource}:{self.action}"


class RolePermission(TimestampMixin, Base):
    """Which permissions a role grants."""

    __tablename__ = "role_permissions"

    role_id: Mapped[uuid.UUID] = mapped_column(
        ForeignKey("roles.id", ondelete="CASCADE"), primary_key=True
    )
    permission_id: Mapped[uuid.UUID] = mapped_column(
        ForeignKey("permissions.id", ondelete="CASCADE"), primary_key=True
    )

    role: Mapped[Role] = relationship(back_populates="permissions")
    permission: Mapped[Permission] = relationship(back_populates="roles")


class UserRole(TimestampMixin, Base):
    """A role granted to a Better Auth user. A user may hold several."""

    __tablename__ = "user_roles"
    __table_args__ = (Index("ix_user_roles_user_id", "user_id"),)

    # A Better Auth user id (cuid). Intentionally unconstrained — see module docstring.
    user_id: Mapped[str] = mapped_column(String(64), primary_key=True)
    role_id: Mapped[uuid.UUID] = mapped_column(
        ForeignKey("roles.id", ondelete="CASCADE"), primary_key=True
    )
    # Who granted it. Null means the seed did.
    assigned_by: Mapped[str | None] = mapped_column(String(64), nullable=True)

    role: Mapped[Role] = relationship(back_populates="users")


class UserInvite(UUIDPk, TimestampMixin, Base):
    """An admin's invitation for one person to hold one role.

    Tensor provisions accounts; it does not let people sign themselves up. An
    admin names an email and a role, and the invitee follows a single-use link
    to set **their own** password. No password is ever chosen for them, shared
    with them, or derived from their role — a role-derived password would be the
    same for everyone holding that role and guessable by anyone who knows the
    pattern, which would also make the audit trail meaningless.

    Only the SHA-256 of the token is stored. The raw token is returned once, at
    creation, and is unrecoverable afterwards: an invite grants a role, so a
    leaked table must not be a leaked account.
    """

    __tablename__ = "user_invites"
    __table_args__ = (Index("ix_user_invites_email", "email"),)

    email: Mapped[str] = mapped_column(String(255))
    role_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("roles.id", ondelete="CASCADE"))

    # SHA-256 hex of the raw token. Unique, so a lookup is a single indexed hit.
    token_hash: Mapped[str] = mapped_column(String(64), unique=True)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))

    # Set once, when the invitee completes the flow. Its presence is what makes
    # the link single-use.
    accepted_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    # The Better Auth user id created on acceptance.
    accepted_user_id: Mapped[str | None] = mapped_column(String(64), nullable=True)

    # Lets an admin cancel an invite before it is used.
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    # The admin who issued it. Null only for invites created by a bootstrap script.
    created_by: Mapped[str | None] = mapped_column(String(64), nullable=True)

    role: Mapped[Role] = relationship()


class UserAuthzState(TimestampMixin, Base):
    """Per-user authorization metadata that is not itself a role grant.

    `permissions_version` is stamped into every access token and bumped whenever
    the user's roles change. Because tokens live ~15 minutes, comparing the claim
    against this value is what lets a permission change be detected before the
    token would naturally expire — without embedding the permission set in the
    token or paying a lookup on every request.
    """

    __tablename__ = "user_authz_state"

    user_id: Mapped[str] = mapped_column(String(64), primary_key=True)
    permissions_version: Mapped[int] = mapped_column(Integer, default=1, server_default="1")
