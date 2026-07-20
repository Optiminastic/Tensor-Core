"""Authorization for Tensor-Core.

Authentication (proving who you are) belongs to Better Auth in the frontend.
This package answers the separate question of what you may do:

    jwt.py           verify a Better Auth token against the frontend's JWKS
    catalog.py       the permission catalog and role -> permission grants
    dependencies.py  FastAPI guards: current_user, require_permission
    service.py       resolve a user's roles/permissions from the database
    models.py        TokenClaims, AuthenticatedUser, UserAuthzResponse

Endpoints depend on `require_permission(...)`, never on a role.
"""

from app.auth.dependencies import (
    CurrentUser,
    current_user,
    require_internal_secret,
    require_permission,
)
from app.auth.models import AuthenticatedUser, TokenClaims, UserAuthzResponse

__all__ = [
    "AuthenticatedUser",
    "CurrentUser",
    "TokenClaims",
    "UserAuthzResponse",
    "current_user",
    "require_internal_secret",
    "require_permission",
]
