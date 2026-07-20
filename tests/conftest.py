"""Shared test fixtures.

Auth is stubbed at the `current_user` seam rather than by minting real tokens.
That keeps `require_permission`'s logic under test — the guard still compares
permissions for real — while token verification itself is tested directly in
tests/test_auth_jwt.py against a real signing key.
"""

from collections.abc import Generator

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from app.auth.catalog import ALL_PERMISSIONS, PermissionSpec
from app.auth.dependencies import current_user
from app.auth.models import AuthenticatedUser
from app.db.base import Base
from app.db.session import get_db
from app.main import app


def make_user(
    *permissions: PermissionSpec | str,
    user_id: str = "usr_test",
    email: str = "test@optiminastic.com",
) -> AuthenticatedUser:
    """An authenticated caller holding exactly the given permissions."""
    keys = frozenset(p.key if isinstance(p, PermissionSpec) else p for p in permissions)
    return AuthenticatedUser(
        id=user_id,
        email=email,
        roles=[],
        permissions=keys,
        permissions_version=1,
        session_id="ses_test",
    )


def admin_user() -> AuthenticatedUser:
    """A caller holding every permission in the catalog."""
    return make_user(*ALL_PERMISSIONS, user_id="usr_admin", email="admin@optiminastic.com")


@pytest.fixture
def db_session() -> Generator[Session, None, None]:
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )
    Base.metadata.create_all(engine)
    testing_session = sessionmaker(bind=engine, autoflush=False, expire_on_commit=False)
    db = testing_session()
    try:
        yield db
    finally:
        db.close()
        engine.dispose()


@pytest.fixture
def make_client(db_session: Session):
    """Build a TestClient authenticated as a given user.

    Pass `user=None` for an anonymous client, which exercises the real 401 path.
    """

    def _build(user: AuthenticatedUser | None = None) -> TestClient:
        def override_get_db() -> Generator[Session, None, None]:
            yield db_session

        app.dependency_overrides[get_db] = override_get_db
        if user is not None:
            app.dependency_overrides[current_user] = lambda: user
        return TestClient(app)

    yield _build
    app.dependency_overrides.clear()


@pytest.fixture
def client(make_client) -> TestClient:
    """The common case: an admin caller. Use `make_client` to vary permissions."""
    return make_client(admin_user())
