"""Resolving a user's authorization state from the database."""

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.models import UserAuthzResponse
from app.db.models.authz import (
    Permission,
    Role,
    RoleName,
    RolePermission,
    UserAuthzState,
    UserRole,
)


def resolve_user_authz(db: Session, user_id: str) -> UserAuthzResponse:
    """The roles, permissions and permissions version for one user.

    Permissions are read from `role_permissions`, not from the in-code catalog.
    The catalog seeds those rows and is the default, but the database is what
    holds at runtime — otherwise a grant could only change on a deploy, and
    `permissions_version` would have nothing to propagate.

    A user with no rows is not an error: they are authenticated but hold
    nothing. Returning empty rather than 404 keeps token minting simple, and an
    empty set means every guard rejects.
    """
    rows = db.execute(
        select(Role.name, Permission.resource, Permission.action)
        .join(UserRole, UserRole.role_id == Role.id)
        .outerjoin(RolePermission, RolePermission.role_id == Role.id)
        .outerjoin(Permission, Permission.id == RolePermission.permission_id)
        .where(UserRole.user_id == user_id)
    ).all()

    roles: set[RoleName] = set()
    permissions: set[str] = set()
    for role_name, resource, action in rows:
        roles.add(RoleName(role_name))
        # outerjoin: a role with no grants yet still yields a row, with nulls.
        if resource is not None and action is not None:
            permissions.add(f"{resource}:{action}")

    version = db.scalar(
        select(UserAuthzState.permissions_version).where(UserAuthzState.user_id == user_id)
    )

    return UserAuthzResponse(
        # Sorted so the token payload is stable between mints; an unordered set
        # would churn the claim and make diffing tokens useless.
        roles=sorted(roles, key=lambda r: r.value),
        permissions=sorted(permissions),
        permissions_version=version if version is not None else 0,
    )


def bump_permissions_version(db: Session, user_id: str) -> int:
    """Invalidate the permission claims on a user's outstanding tokens.

    Must be called by anything that changes a user's roles, or by anything that
    changes what those roles grant. Without it, the change is invisible until
    the current token expires (up to 15 minutes).
    """
    state = db.get(UserAuthzState, user_id)
    if state is None:
        state = UserAuthzState(user_id=user_id, permissions_version=1)
        db.add(state)
        db.flush()
        return state.permissions_version

    state.permissions_version += 1
    db.flush()
    return state.permissions_version
