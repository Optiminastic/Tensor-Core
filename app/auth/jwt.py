"""Verify Better Auth-issued access tokens against the frontend's JWKS.

The frontend is the identity provider. It signs tokens with a key we never hold;
we fetch the public half from its JWKS endpoint and verify. That is why reading
roles from a token is not "trusting the client": the claims are signed by the
issuer and cryptographically checked here before anything reads them.
"""

import threading

import jwt
from jwt import PyJWKClient

from app.auth.exceptions import InvalidTokenError, TokenConfigError
from app.auth.models import TokenClaims
from app.core.config import settings

# Better Auth signs with EdDSA (Ed25519) by default. Pinning the algorithm list
# is what stops an attacker swapping `alg` to `none`, or to an HMAC that our
# public key would then "verify".
_ALLOWED_ALGORITHMS = ["EdDSA", "RS256", "ES256"]

_client_lock = threading.Lock()
_jwks_client: PyJWKClient | None = None


def _get_jwks_client() -> PyJWKClient:
    """The shared JWKS client.

    PyJWKClient keeps its own key cache and refetches when it sees an unknown
    `kid`, so key rotation is handled without restarting the process. Built
    lazily so importing the app never needs the frontend to be reachable.
    """
    global _jwks_client

    if not settings.auth_jwks_url:
        raise TokenConfigError("AUTH_JWKS_URL is not set; cannot verify access tokens")

    with _client_lock:
        if _jwks_client is None:
            _jwks_client = PyJWKClient(
                settings.auth_jwks_url,
                cache_keys=True,
                lifespan=settings.jwks_cache_seconds,
            )
        return _jwks_client


def reset_jwks_client() -> None:
    """Drop the cached client. For tests, and for config reloads."""
    global _jwks_client
    with _client_lock:
        _jwks_client = None


def verify_token(token: str) -> TokenClaims:
    """Verify a bearer token's signature and claims, or raise.

    Checks, in order: the signature against the issuer's public key, then
    `exp`, `iss` and `aud`. A token minted for a different audience is rejected
    even when its signature is perfectly valid.
    """
    if not settings.auth_issuer:
        raise TokenConfigError("AUTH_ISSUER is not set; refusing to verify tokens without it")

    try:
        signing_key = _get_jwks_client().get_signing_key_from_jwt(token)
        payload = jwt.decode(
            token,
            signing_key.key,
            algorithms=_ALLOWED_ALGORITHMS,
            issuer=settings.auth_issuer,
            audience=settings.auth_audience,
            options={
                "require": ["exp", "iss", "aud", "sub"],
                "verify_exp": True,
                "verify_iss": True,
                "verify_aud": True,
                "verify_signature": True,
            },
        )
    except jwt.ExpiredSignatureError as exc:
        raise InvalidTokenError("Access token has expired") from exc
    except jwt.InvalidAudienceError as exc:
        raise InvalidTokenError("Access token was not issued for this service") from exc
    except jwt.InvalidIssuerError as exc:
        raise InvalidTokenError("Access token has an unexpected issuer") from exc
    except jwt.PyJWTError as exc:
        # Everything else — bad signature, malformed token, unknown kid. The
        # reason is logged upstream but never returned to the caller, since it
        # would tell an attacker which part of their forgery to fix.
        raise InvalidTokenError("Access token is invalid") from exc

    return TokenClaims.model_validate(payload)
