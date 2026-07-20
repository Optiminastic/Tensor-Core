"""Request/response shapes for the admin and invite APIs."""

from datetime import datetime

from pydantic import BaseModel, ConfigDict, EmailStr, Field

from app.db.models.authz import RoleName


class InviteCreate(BaseModel):
    """What an admin submits: an email and a role. Nothing else.

    Notably no password field. The invitee chooses their own.
    """

    email: EmailStr
    role: RoleName


class InviteRead(BaseModel):
    """An invite, as listed. Never carries the token."""

    model_config = ConfigDict(from_attributes=True)

    id: str
    email: str
    role: RoleName
    expires_at: datetime
    accepted_at: datetime | None
    revoked_at: datetime | None
    created_by: str | None


class InviteCreated(BaseModel):
    """The one and only time the raw token is returned.

    `token` is not stored anywhere in recoverable form. If the admin loses it,
    the only remedy is to issue a new invite.
    """

    id: str
    email: str
    role: RoleName
    expires_at: datetime
    token: str = Field(description="Shown once. Unrecoverable afterwards.")


class InviteValidated(BaseModel):
    """What the invitee's browser is allowed to learn from a token.

    Deliberately minimal: enough to prefill the form and show who it is for.
    """

    email: str
    role: RoleName


class InviteAccept(BaseModel):
    """Sent after the frontend has created the Better Auth user."""

    token: str = Field(min_length=1)
    user_id: str = Field(min_length=1, max_length=64)


class BootstrapAdmin(BaseModel):
    """Promote the very first user to ADMIN. Works once, ever.

    Closing public sign-up means only an admin can invite, which leaves nobody
    able to create the first admin. This is that escape hatch.
    """

    user_id: str = Field(min_length=1, max_length=64)


class BootstrapStatus(BaseModel):
    """Whether Tensor has an admin yet.

    A hint for /admin's UI only. The authoritative gate is the bootstrap
    endpoint itself, which refuses once an admin exists — checking here alone
    would be a race between two people opening /admin at the same moment.
    """

    admin_exists: bool = Field(serialization_alias="adminExists")

    model_config = ConfigDict(populate_by_name=True)
