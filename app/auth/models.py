"""Shapes for the verified access token and the internal authz contract."""

from pydantic import BaseModel, ConfigDict, Field

from app.db.models.authz import RoleName


class TokenClaims(BaseModel):
    """The claims Better Auth stamps into an access token.

    Validated *after* the signature is verified, so this is trusted data. It is
    still parsed strictly: a token whose shape drifted is a bug we want loudly,
    not a silently empty permission set.
    """

    model_config = ConfigDict(extra="ignore")

    sub: str = Field(min_length=1)
    email: str
    roles: list[RoleName] = Field(default_factory=list)
    permissions: list[str] = Field(default_factory=list)
    permissions_version: int = Field(default=0, alias="permissionsVersion")
    session_id: str | None = Field(default=None, alias="sessionId")


class AuthenticatedUser(BaseModel):
    """The caller, as resolved from a verified token.

    Injected by `app.auth.dependencies.current_user`. Endpoints should not read
    raw claims; they ask this object a question.
    """

    id: str
    email: str
    roles: list[RoleName]
    permissions: frozenset[str]
    permissions_version: int
    session_id: str | None

    def has(self, permission: str) -> bool:
        return permission in self.permissions


class UserAuthzResponse(BaseModel):
    """What /internal/users/{id}/authz returns to Better Auth.

    Field names are camelCase because they cross into the frontend's Zod schema
    (`lib/validators/authz.ts`). Changing them is a breaking contract change.
    """

    roles: list[RoleName]
    permissions: list[str]
    permissions_version: int = Field(serialization_alias="permissionsVersion")

    model_config = ConfigDict(populate_by_name=True)
