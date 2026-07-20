"""Auth failures, separated by who is at fault.

Kept distinct because they map to different responses: a bad token is the
caller's problem (401), missing configuration is ours (500), and a valid token
without the right permission is neither (403).
"""


class AuthError(Exception):
    """Base for everything in this module."""


class InvalidTokenError(AuthError):
    """The token is missing, malformed, expired, or not for us. -> 401."""


class PermissionDeniedError(AuthError):
    """The caller is authenticated but lacks the required permission. -> 403."""

    def __init__(self, permission: str) -> None:
        self.permission = permission
        super().__init__(f"Missing required permission: {permission}")


class TokenConfigError(AuthError):
    """This service is misconfigured and cannot verify tokens. -> 500.

    Deliberately not a 401: telling a caller "unauthorized" when the real
    problem is our own missing AUTH_JWKS_URL sends everyone debugging in the
    wrong direction.
    """
