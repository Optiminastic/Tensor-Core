"""Service-to-service endpoints. Not reachable from a browser.

Guarded by a shared secret rather than a user token, because the caller is the
frontend process itself, resolving roles before any token exists.
"""

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Path, status
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.dependencies import require_internal_secret
from app.auth.invites import InviteError, accept_invite, validate_invite
from app.auth.models import UserAuthzResponse
from app.auth.service import bump_permissions_version, resolve_user_authz
from app.db.models.authz import Role, RoleName, UserRole
from app.db.session import get_db
from app.schemas.admin import BootstrapAdmin, BootstrapStatus, InviteAccept, InviteValidated

router = APIRouter(
    prefix="/internal",
    tags=["internal"],
    dependencies=[Depends(require_internal_secret)],
    # This is a private contract between two of our own processes; publishing it
    # in the OpenAPI schema only advertises it.
    include_in_schema=False,
)


@router.get("/users/{user_id}/authz", response_model=UserAuthzResponse)
def get_user_authz(
    user_id: Annotated[str, Path(min_length=1, max_length=64)],
    db: Annotated[Session, Depends(get_db)],
) -> UserAuthzResponse:
    """Roles and permissions for a user, for stamping into an access token.

    Called by Better Auth's `definePayload` when it mints a token (roughly every
    15 minutes per user), not on every request.

    An unknown user returns empty rather than 404: the frontend has already
    authenticated them, and a user who simply has no role yet is a normal state,
    not an error.
    """
    return resolve_user_authz(db, user_id)


@router.get("/bootstrap-status", response_model=BootstrapStatus)
def bootstrap_status(db: Annotated[Session, Depends(get_db)]) -> BootstrapStatus:
    """Whether an admin exists yet.

    /admin uses this to decide between showing the first-admin sign-up and
    sending people to /login. It is only a hint for the UI — the real gate is
    bootstrap_admin below, which refuses on its own once an admin exists. A UI
    check alone would be a race between two people opening /admin at once.
    """
    return BootstrapStatus(admin_exists=_admin_exists(db))


def _admin_exists(db: Session) -> bool:
    return (
        db.scalar(
            select(UserRole.user_id)
            .join(Role, Role.id == UserRole.role_id)
            .where(Role.name == RoleName.ADMIN.value)
            .limit(1)
        )
        is not None
    )


@router.post("/bootstrap-admin", status_code=status.HTTP_204_NO_CONTENT)
def bootstrap_admin(
    payload: BootstrapAdmin,
    db: Annotated[Session, Depends(get_db)],
) -> None:
    """Grant ADMIN to the very first user. Works exactly once, ever.

    Closing public sign-up creates a chicken-and-egg problem: only an admin can
    invite, so nobody can create the first admin. This is the escape hatch, and
    it is deliberately a one-shot.

    It refuses the moment any ADMIN exists, so it cannot be replayed to
    self-promote later. Combined with the service secret, that means an attacker
    would need both the secret and a database with no admins in it.
    """
    if _admin_exists(db):
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT,
            detail="An admin already exists. Use an invite instead.",
        )

    admin_role = db.scalar(select(Role).where(Role.name == RoleName.ADMIN.value))
    if admin_role is None:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="Roles are not seeded. Run: uv run python -m app.auth.seed",
        )

    db.add(UserRole(user_id=payload.user_id, role_id=admin_role.id))
    bump_permissions_version(db, payload.user_id)
    db.commit()


@router.get("/invites/{token}", response_model=InviteValidated)
def check_invite(
    token: Annotated[str, Path(min_length=1, max_length=128)],
    db: Annotated[Session, Depends(get_db)],
) -> InviteValidated:
    """Check an invite token and reveal who it is for.

    Called before any user or session exists, which is why it is gated by the
    service secret rather than a bearer token.

    Every failure is the same 400 with the same message. Distinguishing expired
    from used from never-existed would confirm to a stranger which tokens are
    real.
    """
    try:
        invite = validate_invite(db, token)
    except InviteError as exc:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(exc)) from exc

    role_name = db.get(Role, invite.role_id)
    if role_name is None:
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="The invited role no longer exists",
        )

    return InviteValidated(email=invite.email, role=RoleName(role_name.name))


@router.post("/invites/accept", status_code=status.HTTP_204_NO_CONTENT)
def redeem_invite(
    payload: InviteAccept,
    db: Annotated[Session, Depends(get_db)],
) -> None:
    """Redeem an invite for a newly created user and attach the role.

    Called by the frontend immediately after it creates the Better Auth user, so
    `user_id` already exists. Burning the invite and granting the role happen in
    one transaction — a half-applied invite would either leave a live link that
    was already used, or a used link that granted nothing.
    """
    try:
        accept_invite(db, raw_token=payload.token, user_id=payload.user_id)
    except InviteError as exc:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(exc)) from exc
    db.commit()
