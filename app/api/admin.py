"""Admin API — provisioning people and their roles.

Tensor does not let people sign themselves up. An admin names an email and a
role here, and the invitee follows a single-use link to set their own password.
Everything on this router requires `user:manage`.
"""

import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import USER_MANAGE
from app.auth.dependencies import CurrentUser, require_permission
from app.auth.invites import InviteError, issue_invite, revoke_invite
from app.db.models.authz import Role, UserInvite
from app.db.session import get_db
from app.schemas.admin import InviteCreate, InviteCreated, InviteRead

router = APIRouter(
    prefix="/admin",
    tags=["admin"],
    dependencies=[Depends(require_permission(USER_MANAGE))],
)


def _to_read(invite: UserInvite, role_name: str) -> InviteRead:
    return InviteRead(
        id=str(invite.id),
        email=invite.email,
        role=role_name,
        expires_at=invite.expires_at,
        accepted_at=invite.accepted_at,
        revoked_at=invite.revoked_at,
        created_by=invite.created_by,
    )


@router.post("/invites", response_model=InviteCreated, status_code=status.HTTP_201_CREATED)
def create_invite(
    payload: InviteCreate,
    user: CurrentUser,
    db: Annotated[Session, Depends(get_db)],
) -> InviteCreated:
    """Invite someone to hold a role.

    Returns the raw token **once**. It is stored only as a SHA-256 hash, so it
    cannot be shown again — if the admin loses it, issue a new invite.

    Re-inviting an email supersedes any earlier pending invite, so a link sent
    to the wrong person stops working immediately.
    """
    try:
        invite, raw_token = issue_invite(
            db, email=payload.email, role=payload.role, created_by=user.id
        )
    except InviteError as exc:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(exc)) from exc

    db.commit()

    return InviteCreated(
        id=str(invite.id),
        email=invite.email,
        role=payload.role,
        expires_at=invite.expires_at,
        token=raw_token,
    )


@router.get("/invites", response_model=list[InviteRead])
def list_invites(db: Annotated[Session, Depends(get_db)]) -> list[InviteRead]:
    """Every invite, newest first. Tokens are never included."""
    rows = db.execute(
        select(UserInvite, Role.name)
        .join(Role, Role.id == UserInvite.role_id)
        .order_by(UserInvite.created_at.desc())
    ).all()
    return [_to_read(invite, role_name) for invite, role_name in rows]


@router.post("/invites/{invite_id}/revoke", status_code=status.HTTP_204_NO_CONTENT)
def revoke(invite_id: uuid.UUID, db: Annotated[Session, Depends(get_db)]) -> None:
    """Cancel a pending invite, so its link stops working."""
    try:
        revoke_invite(db, invite_id)
    except InviteError as exc:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail=str(exc)) from exc
    db.commit()
