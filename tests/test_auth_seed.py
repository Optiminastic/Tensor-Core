"""Seeding the catalog into the database."""

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from app.auth.catalog import ALL_PERMISSIONS, ROLE_PERMISSIONS, permissions_for
from app.auth.seed import sync_all, sync_role_permissions
from app.auth.service import resolve_user_authz
from app.db.models.authz import Permission, Role, RoleName, RolePermission, UserRole


def test_sync_creates_the_whole_catalog(db_session: Session) -> None:
    result = sync_all(db_session)
    db_session.commit()

    assert result["permissions"] == len(ALL_PERMISSIONS)
    assert result["roles"] == len(ROLE_PERMISSIONS)
    assert db_session.scalar(select(func.count()).select_from(Permission)) == len(ALL_PERMISSIONS)
    assert db_session.scalar(select(func.count()).select_from(Role)) == len(ROLE_PERMISSIONS)


def test_sync_is_idempotent(db_session: Session) -> None:
    """It runs on every deploy, so a second run must not duplicate or churn."""
    first = sync_all(db_session)
    db_session.commit()
    grants_after_first = db_session.scalar(select(func.count()).select_from(RolePermission))

    second = sync_all(db_session)
    db_session.commit()

    assert first == second
    assert db_session.scalar(select(func.count()).select_from(RolePermission)) == grants_after_first
    assert db_session.scalar(select(func.count()).select_from(Permission)) == len(ALL_PERMISSIONS)


def test_sync_revokes_a_grant_the_catalog_no_longer_makes(db_session: Session) -> None:
    """The dangerous direction: a stale row would keep granting silently."""
    sync_all(db_session)
    db_session.commit()

    operator = db_session.scalar(select(Role).where(Role.name == RoleName.OPERATOR.value))
    config_manage = db_session.scalar(
        select(Permission).where(Permission.resource == "config", Permission.action == "manage")
    )
    assert operator is not None and config_manage is not None

    # Simulate drift: someone granted the Operator cost access directly in SQL.
    db_session.add(RolePermission(role_id=operator.id, permission_id=config_manage.id))
    db_session.commit()

    db_session.add(UserRole(user_id="usr_op", role_id=operator.id))
    db_session.commit()
    assert "config:manage" in resolve_user_authz(db_session, "usr_op").permissions

    # Re-syncing must take it back, because the catalog does not grant it.
    sync_role_permissions(db_session)
    db_session.commit()

    assert "config:manage" not in resolve_user_authz(db_session, "usr_op").permissions


def test_seeded_grants_match_the_catalog(db_session: Session) -> None:
    """The database projection agrees with the in-code source of truth."""
    sync_all(db_session)
    db_session.commit()

    for role_name in ROLE_PERMISSIONS:
        role = db_session.scalar(select(Role).where(Role.name == role_name.value))
        assert role is not None
        db_session.add(UserRole(user_id=f"usr_{role_name.value}", role_id=role.id))
    db_session.commit()

    for role_name in ROLE_PERMISSIONS:
        resolved = resolve_user_authz(db_session, f"usr_{role_name.value}")
        assert set(resolved.permissions) == permissions_for(role_name), role_name
