"""FastAPI dependencies for authentication and authorization.

Endpoints never parse tokens or check roles themselves. They declare what they
need:

    @router.post("/materials", dependencies=[Depends(require_permission(CONFIG_MANAGE))])

and the guard runs before the handler.
"""

import secrets
from collections.abc import Callable
from typing import Annotated

from fastapi import Depends, Header, HTTPException, Request, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from app.auth.catalog import PermissionSpec
from app.auth.exceptions import InvalidTokenError, TokenConfigError
from app.auth.jwt import verify_token
from app.auth.models import AuthenticatedUser
from app.core.config import settings

# auto_error=False so a missing header reaches our handler and returns a
# consistent body, rather than FastAPI's default shape.
_bearer = HTTPBearer(auto_error=False, description="Better Auth-issued access token")


def current_user(
    credentials: Annotated[HTTPAuthorizationCredentials | None, Depends(_bearer)],
) -> AuthenticatedUser:
    """Resolve the caller from a verified access token, or raise 401."""
    if credentials is None:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Missing bearer token",
            headers={"WWW-Authenticate": "Bearer"},
        )

    try:
        claims = verify_token(credentials.credentials)
    except TokenConfigError as exc:
        # Our fault, not the caller's. Never dressed up as a 401.
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="Authentication is not configured on this service",
        ) from exc
    except InvalidTokenError as exc:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail=str(exc),
            headers={"WWW-Authenticate": "Bearer"},
        ) from exc

    return AuthenticatedUser(
        id=claims.sub,
        email=claims.email,
        roles=claims.roles,
        permissions=frozenset(claims.permissions),
        permissions_version=claims.permissions_version,
        session_id=claims.session_id,
    )


CurrentUser = Annotated[AuthenticatedUser, Depends(current_user)]


def require_permission(permission: PermissionSpec | str) -> Callable[..., AuthenticatedUser]:
    """Build a dependency that requires one permission.

    Takes a `PermissionSpec` from the catalog rather than a bare string so a
    typo is an import error instead of an endpoint that silently never grants.
    """
    key = permission.key if isinstance(permission, PermissionSpec) else permission

    def _guard(user: CurrentUser) -> AuthenticatedUser:
        if not user.has(key):
            # 403, not 404: the caller is known, they simply may not do this.
            raise HTTPException(
                status_code=status.HTTP_403_FORBIDDEN,
                detail=f"Missing required permission: {key}",
            )
        return user

    return _guard


def require_internal_secret(
    request: Request,
    x_internal_secret: Annotated[str | None, Header(alias="X-Internal-Secret")] = None,
) -> None:
    """Gate the internal, service-to-service API.

    Guards the role lookup the frontend calls when minting a token. This is not
    a user-facing endpoint and must never be reachable from a browser.
    """
    if not settings.internal_api_secret:
        # No secret configured means closed, not open. A deployment that forgot
        # to set it should fail loudly rather than expose every user's roles.
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="Internal API is not configured",
        )

    # Constant-time: a plain `!=` leaks the secret one byte at a time to anyone
    # who can measure response latency.
    if not x_internal_secret or not secrets.compare_digest(
        x_internal_secret, settings.internal_api_secret
    ):
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="Invalid internal secret",
        )

    _ = request  # Reserved for future IP allow-listing.
