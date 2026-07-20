"""The invite flow.

The security properties, not just the happy path: a link must work exactly once,
must expire, must never be recoverable from the database, and must carry the
role it promised.
"""

from datetime import UTC, datetime, timedelta

import pytest
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import USER_MANAGE, permissions_for
from app.auth.invites import (
    InviteError,
    accept_invite,
    issue_invite,
    revoke_invite,
    validate_invite,
)
from app.auth.seed import sync_all
from app.auth.service import resolve_user_authz
from app.core.config import settings
from app.db.models.authz import RoleName, UserInvite
from tests.conftest import make_user

SECRET = "test-internal-secret-that-is-long-enough-32"


@pytest.fixture(autouse=True)
def _internal_secret(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(settings, "internal_api_secret", SECRET)


@pytest.fixture
def seeded(db_session: Session) -> Session:
    sync_all(db_session)
    db_session.commit()
    return db_session


# ── The token ────────────────────────────────────────────────────────


def test_raw_token_is_never_stored(seeded: Session) -> None:
    """The database must not be able to hand anyone a working link."""
    invite, raw_token = issue_invite(
        seeded, email="designer@opti.com", role=RoleName.DESIGNER, created_by="usr_admin"
    )
    seeded.commit()

    assert invite.token_hash != raw_token
    assert len(invite.token_hash) == 64  # sha256 hex

    # The raw token appears nowhere in the row.
    stored = seeded.scalar(select(UserInvite).where(UserInvite.id == invite.id))
    assert stored is not None
    assert raw_token not in str(stored.__dict__)


def test_tokens_are_unique_per_invite(seeded: Session) -> None:
    _, first = issue_invite(seeded, email="a@opti.com", role=RoleName.DESIGNER, created_by="x")
    _, second = issue_invite(seeded, email="b@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    assert first != second


def test_email_is_normalised(seeded: Session) -> None:
    invite, _ = issue_invite(
        seeded, email="  Designer@Opti.com  ", role=RoleName.DESIGNER, created_by="x"
    )
    seeded.commit()
    assert invite.email == "designer@opti.com"


# ── Validation ───────────────────────────────────────────────────────


def test_valid_token_resolves(seeded: Session) -> None:
    issued, raw = issue_invite(
        seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="usr_admin"
    )
    seeded.commit()
    assert validate_invite(seeded, raw).id == issued.id


def test_unknown_token_is_rejected(seeded: Session) -> None:
    with pytest.raises(InviteError):
        validate_invite(seeded, "not-a-real-token")


def test_expired_token_is_rejected(seeded: Session) -> None:
    _, raw = issue_invite(
        seeded,
        email="d@opti.com",
        role=RoleName.DESIGNER,
        created_by="x",
        ttl=timedelta(hours=-1),
    )
    seeded.commit()
    with pytest.raises(InviteError):
        validate_invite(seeded, raw)


def test_failures_are_indistinguishable(seeded: Session) -> None:
    """Expired, used and never-existed must not be tellable apart.

    A different message per case would confirm to a stranger which tokens real.
    """
    _, expired = issue_invite(
        seeded, email="a@opti.com", role=RoleName.DESIGNER, created_by="x", ttl=timedelta(hours=-1)
    )
    _, used = issue_invite(seeded, email="b@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    accept_invite(seeded, raw_token=used, user_id="usr_b")
    seeded.commit()

    messages = set()
    for token in (expired, used, "never-existed"):
        with pytest.raises(InviteError) as exc:
            validate_invite(seeded, token)
        messages.add(str(exc.value))

    assert len(messages) == 1, f"leaked which token is which: {messages}"


# ── Acceptance ───────────────────────────────────────────────────────


def test_accepting_attaches_the_role(seeded: Session) -> None:
    _, raw = issue_invite(
        seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="usr_admin"
    )
    seeded.commit()

    accept_invite(seeded, raw_token=raw, user_id="usr_new")
    seeded.commit()

    authz = resolve_user_authz(seeded, "usr_new")
    assert authz.roles == [RoleName.DESIGNER]
    assert set(authz.permissions) == permissions_for(RoleName.DESIGNER)
    # Without the bump, the grant is invisible to any token already minted.
    assert authz.permissions_version >= 1


def test_a_link_works_exactly_once(seeded: Session) -> None:
    """The whole point of a single-use link."""
    _, raw = issue_invite(seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()

    accept_invite(seeded, raw_token=raw, user_id="usr_first")
    seeded.commit()

    with pytest.raises(InviteError):
        accept_invite(seeded, raw_token=raw, user_id="usr_second")

    # The second person got nothing.
    assert resolve_user_authz(seeded, "usr_second").roles == []


def test_expired_link_cannot_be_accepted(seeded: Session) -> None:
    _, raw = issue_invite(
        seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="x", ttl=timedelta(hours=-1)
    )
    seeded.commit()
    with pytest.raises(InviteError):
        accept_invite(seeded, raw_token=raw, user_id="usr_late")
    assert resolve_user_authz(seeded, "usr_late").roles == []


def test_reinviting_supersedes_the_old_link(seeded: Session) -> None:
    """A link sent to the wrong person must stop working once re-issued."""
    _, old = issue_invite(seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    _, new = issue_invite(seeded, email="d@opti.com", role=RoleName.PROJECT_LEAD, created_by="x")
    seeded.commit()

    with pytest.raises(InviteError):
        validate_invite(seeded, old)
    assert validate_invite(seeded, new).email == "d@opti.com"


def test_revoked_link_stops_working(seeded: Session) -> None:
    invite, raw = issue_invite(seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    revoke_invite(seeded, invite.id)
    seeded.commit()
    with pytest.raises(InviteError):
        validate_invite(seeded, raw)


def test_accept_is_idempotent_on_the_role_but_not_the_link(seeded: Session) -> None:
    """Re-granting a role a user already holds must not blow up on the PK."""
    _, first = issue_invite(seeded, email="a@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    accept_invite(seeded, raw_token=first, user_id="usr_1")
    seeded.commit()

    _, second = issue_invite(seeded, email="a@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    accept_invite(seeded, raw_token=second, user_id="usr_1")
    seeded.commit()

    assert resolve_user_authz(seeded, "usr_1").roles == [RoleName.DESIGNER]


# ── The endpoints ────────────────────────────────────────────────────


def test_creating_an_invite_requires_user_manage(make_client, seeded: Session) -> None:
    designer = make_client(make_user("design:create"))
    resp = designer.post("/admin/invites", json={"email": "x@opti.com", "role": "DESIGNER"})
    assert resp.status_code == 403


def test_anonymous_cannot_create_an_invite(make_client) -> None:
    assert (
        make_client(None)
        .post("/admin/invites", json={"email": "x@opti.com", "role": "DESIGNER"})
        .status_code
        == 401
    )


def test_admin_creates_an_invite_and_gets_the_token_once(make_client, seeded: Session) -> None:
    admin = make_client(make_user(USER_MANAGE, user_id="usr_admin"))
    resp = admin.post("/admin/invites", json={"email": "designer@opti.com", "role": "DESIGNER"})

    assert resp.status_code == 201
    body = resp.json()
    assert body["email"] == "designer@opti.com"
    assert body["role"] == "DESIGNER"
    assert len(body["token"]) > 20

    # Listing must never leak it again.
    listed = admin.get("/admin/invites").json()
    assert len(listed) == 1
    assert "token" not in listed[0]


def test_invite_rejects_a_bad_email(make_client, seeded: Session) -> None:
    admin = make_client(make_user(USER_MANAGE))
    resp = admin.post("/admin/invites", json={"email": "not-an-email", "role": "DESIGNER"})
    assert resp.status_code == 422


def test_invite_rejects_an_unknown_role(make_client, seeded: Session) -> None:
    admin = make_client(make_user(USER_MANAGE))
    resp = admin.post("/admin/invites", json={"email": "a@opti.com", "role": "SUPERUSER"})
    assert resp.status_code == 422


def test_internal_check_and_accept(make_client, seeded: Session) -> None:
    admin = make_client(make_user(USER_MANAGE, user_id="usr_admin"))
    token = admin.post(
        "/admin/invites", json={"email": "lead@opti.com", "role": "PROJECT_LEAD"}
    ).json()["token"]

    anon = make_client(None)
    headers = {"X-Internal-Secret": SECRET}

    checked = anon.get(f"/internal/invites/{token}", headers=headers)
    assert checked.status_code == 200
    assert checked.json() == {"email": "lead@opti.com", "role": "PROJECT_LEAD"}

    accepted = anon.post(
        "/internal/invites/accept",
        json={"token": token, "user_id": "usr_lead"},
        headers=headers,
    )
    assert accepted.status_code == 204

    # The role landed, and the link is spent.
    assert anon.get(f"/internal/invites/{token}", headers=headers).status_code == 400


def test_invite_endpoints_need_the_internal_secret(make_client, seeded: Session) -> None:
    anon = make_client(None)
    assert anon.get("/internal/invites/whatever").status_code == 403
    assert (
        anon.post("/internal/invites/accept", json={"token": "x", "user_id": "y"}).status_code
        == 403
    )


# ── Bootstrap ────────────────────────────────────────────────────────


def test_bootstrap_promotes_the_first_user(make_client, seeded: Session) -> None:
    anon = make_client(None)
    resp = anon.post(
        "/internal/bootstrap-admin",
        json={"user_id": "usr_first"},
        headers={"X-Internal-Secret": SECRET},
    )
    assert resp.status_code == 204
    assert resolve_user_authz(seeded, "usr_first").roles == [RoleName.ADMIN]


def test_bootstrap_cannot_be_replayed(make_client, seeded: Session) -> None:
    """The one that matters: it must not become a self-promotion endpoint."""
    anon = make_client(None)
    headers = {"X-Internal-Secret": SECRET}

    assert (
        anon.post(
            "/internal/bootstrap-admin", json={"user_id": "usr_first"}, headers=headers
        ).status_code
        == 204
    )

    replay = anon.post(
        "/internal/bootstrap-admin", json={"user_id": "usr_attacker"}, headers=headers
    )
    assert replay.status_code == 409
    assert resolve_user_authz(seeded, "usr_attacker").roles == []


def test_bootstrap_needs_the_internal_secret(make_client, seeded: Session) -> None:
    resp = make_client(None).post("/internal/bootstrap-admin", json={"user_id": "usr_x"})
    assert resp.status_code == 403


def test_invite_expiry_defaults_to_72h(seeded: Session) -> None:
    invite, _ = issue_invite(seeded, email="d@opti.com", role=RoleName.DESIGNER, created_by="x")
    seeded.commit()
    expires_at = invite.expires_at
    if expires_at.tzinfo is None:
        expires_at = expires_at.replace(tzinfo=UTC)
    delta = expires_at - datetime.now(UTC)
    assert timedelta(hours=71) < delta <= timedelta(hours=72)
