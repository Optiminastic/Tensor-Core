"""The internal role-lookup endpoint and the catalog behind it."""

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import (
    ALL_PERMISSIONS,
    BRAND_MANAGE,
    BRAND_READ,
    CONFIG_MANAGE,
    CONFIG_READ,
    DESIGN_APPROVE,
    PRICING_READ,
    PROJECT_MANAGE,
    PROJECT_READ,
    USER_MANAGE,
    permissions_for,
    permissions_for_roles,
)
from app.auth.seed import sync_all
from app.auth.service import bump_permissions_version, resolve_user_authz
from app.core.config import settings
from app.db.models.authz import Permission, Role, RoleName, RolePermission, UserRole
from app.main import app

SECRET = "test-internal-secret-that-is-long-enough-32"


@pytest.fixture(autouse=True)
def _internal_secret(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(settings, "internal_api_secret", SECRET)


def _grant(db: Session, user_id: str, name: RoleName) -> None:
    """Seed the catalog and give a user a role.

    Permissions resolve from `role_permissions` at runtime, so the catalog has
    to actually be in the database — the in-code defaults are not consulted.
    """
    sync_all(db)
    role = db.scalar(select(Role).where(Role.name == name.value))
    assert role is not None
    db.add(UserRole(user_id=user_id, role_id=role.id))
    db.flush()


# ── The catalog ──────────────────────────────────────────────────────


def test_admin_holds_every_permission() -> None:
    """Admin is defined as the whole catalog, so new permissions are never
    accidentally withheld from it."""
    assert permissions_for(RoleName.ADMIN) == frozenset(p.key for p in ALL_PERMISSIONS)


def test_operator_cannot_see_costs() -> None:
    """A documented separation of duties, asserted rather than assumed."""
    operator = permissions_for(RoleName.OPERATOR)
    assert CONFIG_READ.key not in operator
    assert CONFIG_MANAGE.key not in operator
    assert PRICING_READ.key not in operator


def test_designer_cannot_approve_or_price() -> None:
    designer = permissions_for(RoleName.DESIGNER)
    assert DESIGN_APPROVE.key not in designer
    assert PRICING_READ.key not in designer


def test_project_lead_cannot_manage_users() -> None:
    assert USER_MANAGE.key not in permissions_for(RoleName.PROJECT_LEAD)


def test_only_admin_manages_projects() -> None:
    """Phase 1: projects are admin-only. No operational role holds project:* yet."""
    assert PROJECT_MANAGE.key in permissions_for(RoleName.ADMIN)
    assert PROJECT_READ.key in permissions_for(RoleName.ADMIN)
    for role in (
        RoleName.DESIGNER,
        RoleName.PROJECT_LEAD,
        RoleName.PERFORMANCE_MARKETER,
        RoleName.OPERATOR,
    ):
        assert PROJECT_MANAGE.key not in permissions_for(role)
        assert PROJECT_READ.key not in permissions_for(role)


def test_only_admin_manages_brands() -> None:
    """Editing pricing ladders is admin-only. No operational role holds brand:*."""
    assert BRAND_MANAGE.key in permissions_for(RoleName.ADMIN)
    assert BRAND_READ.key in permissions_for(RoleName.ADMIN)
    for role in (
        RoleName.DESIGNER,
        RoleName.PROJECT_LEAD,
        RoleName.PERFORMANCE_MARKETER,
        RoleName.OPERATOR,
    ):
        assert BRAND_MANAGE.key not in permissions_for(role)
        assert BRAND_READ.key not in permissions_for(role)


def test_multiple_roles_union_their_permissions() -> None:
    """A user may hold several roles, e.g. Lead + Marketer."""
    both = permissions_for_roles([RoleName.PROJECT_LEAD, RoleName.PERFORMANCE_MARKETER])
    assert both == permissions_for(RoleName.PROJECT_LEAD) | permissions_for(
        RoleName.PERFORMANCE_MARKETER
    )


def test_no_roles_grants_nothing() -> None:
    assert permissions_for_roles([]) == frozenset()


# ── Resolving a user ─────────────────────────────────────────────────


def test_resolve_unknown_user_returns_empty_not_error(db_session: Session) -> None:
    """A user with no role is normal, not a 404."""
    result = resolve_user_authz(db_session, "usr_nobody")
    assert result.roles == []
    assert result.permissions == []
    assert result.permissions_version == 0


def test_resolve_user_returns_role_permissions(db_session: Session) -> None:
    _grant(db_session, "usr_1", RoleName.DESIGNER)
    db_session.commit()

    result = resolve_user_authz(db_session, "usr_1")

    assert result.roles == [RoleName.DESIGNER]
    # The seeded rows agree with the catalog's defaults.
    assert set(result.permissions) == permissions_for(RoleName.DESIGNER)
    # Sorted so the token payload is stable between mints.
    assert result.permissions == sorted(result.permissions)


def test_resolve_reads_grants_from_the_database_not_the_catalog(db_session: Session) -> None:
    """The database holds at runtime, so a grant can change without a deploy.

    This is what makes permissions_version meaningful: if grants were compiled
    in, there would be nothing to propagate between deploys.
    """
    _grant(db_session, "usr_1", RoleName.OPERATOR)
    db_session.commit()
    assert "config:read" not in resolve_user_authz(db_session, "usr_1").permissions

    operator = db_session.scalar(select(Role).where(Role.name == RoleName.OPERATOR.value))
    config_read = db_session.scalar(
        select(Permission).where(Permission.resource == "config", Permission.action == "read")
    )
    assert operator is not None and config_read is not None
    db_session.add(RolePermission(role_id=operator.id, permission_id=config_read.id))
    db_session.commit()

    assert "config:read" in resolve_user_authz(db_session, "usr_1").permissions


def test_role_with_no_grants_still_resolves_as_a_role(db_session: Session) -> None:
    """The outer join must not drop a user whose role grants nothing yet."""
    role = Role(name=RoleName.OPERATOR.value, description="no grants yet")
    db_session.add(role)
    db_session.flush()
    db_session.add(UserRole(user_id="usr_bare", role_id=role.id))
    db_session.commit()

    result = resolve_user_authz(db_session, "usr_bare")
    assert result.roles == [RoleName.OPERATOR]
    assert result.permissions == []


def test_bump_permissions_version_increments(db_session: Session) -> None:
    assert bump_permissions_version(db_session, "usr_1") == 1
    assert bump_permissions_version(db_session, "usr_1") == 2
    db_session.commit()
    assert resolve_user_authz(db_session, "usr_1").permissions_version == 2


# ── The endpoint ─────────────────────────────────────────────────────


def test_internal_endpoint_requires_the_secret(make_client) -> None:
    client: TestClient = make_client(None)
    assert client.get("/internal/users/usr_1/authz").status_code == 403


def test_internal_endpoint_rejects_a_wrong_secret(make_client) -> None:
    client: TestClient = make_client(None)
    resp = client.get("/internal/users/usr_1/authz", headers={"X-Internal-Secret": "nope"})
    assert resp.status_code == 403


def test_internal_endpoint_returns_camel_case_for_the_frontend(
    make_client, db_session: Session
) -> None:
    """`permissionsVersion` is the wire contract with lib/validators/authz.ts."""
    _grant(db_session, "usr_lead", RoleName.PROJECT_LEAD)
    bump_permissions_version(db_session, "usr_lead")
    db_session.commit()

    client: TestClient = make_client(None)
    resp = client.get("/internal/users/usr_lead/authz", headers={"X-Internal-Secret": SECRET})

    assert resp.status_code == 200
    body = resp.json()
    assert body["roles"] == ["PROJECT_LEAD"]
    assert "permissionsVersion" in body, "frontend Zod schema expects camelCase"
    assert body["permissionsVersion"] == 1
    assert DESIGN_APPROVE.key in body["permissions"]


def test_internal_endpoint_closed_when_no_secret_configured(
    make_client, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A deployment that forgot the secret must fail closed, not open."""
    monkeypatch.setattr(settings, "internal_api_secret", None)
    client: TestClient = make_client(None)
    resp = client.get("/internal/users/usr_1/authz", headers={"X-Internal-Secret": SECRET})
    assert resp.status_code == 503


def test_internal_endpoint_is_not_in_the_public_schema() -> None:
    """It is a private contract between our own processes."""
    paths = app.openapi()["paths"]
    assert not any(p.startswith("/internal") for p in paths)
