"""Token verification, against real Ed25519 signatures.

These are the tests that matter most in this module: everything else in the
service trusts `verify_token` to have already refused forgeries, so each way a
token can be wrong gets an explicit case.
"""

from datetime import UTC, datetime, timedelta
from typing import Any

import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from app.auth import jwt as auth_jwt
from app.auth.exceptions import InvalidTokenError, TokenConfigError
from app.core.config import settings

ISSUER = "http://localhost:3000"
AUDIENCE = "tensor-core"


@pytest.fixture
def signing_key() -> Ed25519PrivateKey:
    """The issuer's key. Better Auth signs with EdDSA/Ed25519 by default."""
    return Ed25519PrivateKey.generate()


@pytest.fixture(autouse=True)
def _auth_settings(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(settings, "auth_issuer", ISSUER)
    monkeypatch.setattr(settings, "auth_audience", AUDIENCE)
    monkeypatch.setattr(settings, "auth_jwks_url", "http://localhost:3000/api/auth/jwks")
    auth_jwt.reset_jwks_client()


@pytest.fixture(autouse=True)
def _stub_jwks(monkeypatch: pytest.MonkeyPatch, signing_key: Ed25519PrivateKey) -> None:
    """Serve the issuer's public key without going over the network.

    Only key *delivery* is stubbed. The signature check itself is real.
    """
    public_key = signing_key.public_key()

    class _FakeJWK:
        key = public_key

    class _FakeClient:
        def get_signing_key_from_jwt(self, token: str) -> _FakeJWK:  # noqa: ARG002
            return _FakeJWK()

    monkeypatch.setattr(auth_jwt, "_get_jwks_client", lambda: _FakeClient())


def make_token(
    signing_key: Ed25519PrivateKey,
    *,
    issuer: str = ISSUER,
    audience: str = AUDIENCE,
    expires_in: timedelta = timedelta(minutes=15),
    **overrides: Any,
) -> str:
    now = datetime.now(UTC)
    payload: dict[str, Any] = {
        "sub": "usr_abc123",
        "email": "designer@optiminastic.com",
        "roles": ["DESIGNER"],
        "permissions": ["design:create", "design:read"],
        "permissionsVersion": 3,
        "sessionId": "ses_xyz",
        "iss": issuer,
        "aud": audience,
        "iat": now,
        "exp": now + expires_in,
    }
    payload.update(overrides)
    return jwt.encode(payload, signing_key, algorithm="EdDSA")


def test_valid_token_yields_claims(signing_key: Ed25519PrivateKey) -> None:
    claims = auth_jwt.verify_token(make_token(signing_key))

    assert claims.sub == "usr_abc123"
    assert claims.email == "designer@optiminastic.com"
    assert claims.permissions == ["design:create", "design:read"]
    # camelCase on the wire, snake_case in Python.
    assert claims.permissions_version == 3
    assert claims.session_id == "ses_xyz"


def test_expired_token_is_rejected(signing_key: Ed25519PrivateKey) -> None:
    token = make_token(signing_key, expires_in=timedelta(minutes=-1))
    with pytest.raises(InvalidTokenError, match="expired"):
        auth_jwt.verify_token(token)


def test_token_for_another_audience_is_rejected(signing_key: Ed25519PrivateKey) -> None:
    """A perfectly-signed token minted for a different service is not ours."""
    token = make_token(signing_key, audience="some-other-service")
    with pytest.raises(InvalidTokenError, match="not issued for this service"):
        auth_jwt.verify_token(token)


def test_token_from_another_issuer_is_rejected(signing_key: Ed25519PrivateKey) -> None:
    token = make_token(signing_key, issuer="https://evil.example.com")
    with pytest.raises(InvalidTokenError, match="unexpected issuer"):
        auth_jwt.verify_token(token)


def test_token_signed_by_a_different_key_is_rejected() -> None:
    """The core forgery case: right shape, wrong key."""
    attacker_key = Ed25519PrivateKey.generate()
    token = make_token(attacker_key)
    with pytest.raises(InvalidTokenError):
        auth_jwt.verify_token(token)


def test_unsigned_token_is_rejected(signing_key: Ed25519PrivateKey) -> None:
    """`alg: none` must never be honoured."""
    now = datetime.now(UTC)
    token = jwt.encode(
        {
            "sub": "usr_attacker",
            "email": "attacker@evil.com",
            "permissions": ["config:manage", "pricing:override"],
            "iss": ISSUER,
            "aud": AUDIENCE,
            "iat": now,
            "exp": now + timedelta(minutes=15),
        },
        key="",
        algorithm="none",
    )
    with pytest.raises(InvalidTokenError):
        auth_jwt.verify_token(token)


def test_token_without_expiry_is_rejected(signing_key: Ed25519PrivateKey) -> None:
    """A token that never expires defeats the 15-minute window entirely."""
    token = jwt.encode(
        {
            "sub": "usr_abc123",
            "email": "designer@optiminastic.com",
            "iss": ISSUER,
            "aud": AUDIENCE,
        },
        signing_key,
        algorithm="EdDSA",
    )
    with pytest.raises(InvalidTokenError):
        auth_jwt.verify_token(token)


def test_missing_issuer_config_raises_config_error(
    signing_key: Ed25519PrivateKey, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Our misconfiguration is a 500, never a 401 blamed on the caller."""
    monkeypatch.setattr(settings, "auth_issuer", None)
    with pytest.raises(TokenConfigError):
        auth_jwt.verify_token(make_token(signing_key))


def test_token_with_no_permissions_parses_but_grants_nothing(
    signing_key: Ed25519PrivateKey,
) -> None:
    """The fail-closed path: the frontend could not reach us, so it minted empty."""
    claims = auth_jwt.verify_token(make_token(signing_key, roles=[], permissions=[]))
    assert claims.permissions == []
    assert claims.roles == []
