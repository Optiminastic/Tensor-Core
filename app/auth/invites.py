"""Issuing and redeeming invitations.

The flow, and why it is shaped this way:

    admin names an email + role   ->  issue_invite()  -> raw token, once
    admin hands the link over     ->  (Slack, in person; never emailed)
    invitee opens the link        ->  validate_invite()
    invitee sets THEIR OWN password -> accept_invite() -> role attached

No password is ever generated for the invitee, shown to the admin, or derived
from the role. A role-derived password would be identical for everyone holding
that role and guessable by anyone who has seen the pattern, including people who
have left — and it would make "approved by X" unprovable, which defeats the
point of auditing at all.
"""

import hashlib
import secrets
import uuid
from datetime import UTC, datetime, timedelta

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.service import bump_permissions_version
from app.db.models.authz import Role, RoleName, UserInvite, UserRole

# Long enough that guessing is hopeless; the token is the only thing standing
# between a stranger and a provisioned account.
_TOKEN_BYTES = 32

DEFAULT_INVITE_TTL = timedelta(hours=72)


class InviteError(Exception):
    """An invite cannot be used. The message is safe to show the invitee."""


def _hash_token(raw_token: str) -> str:
    """SHA-256 of the raw token.

    A plain hash, not a password KDF, is correct here: the token is 32 bytes of
    CSPRNG output, so there is no dictionary to attack and nothing for bcrypt's
    work factor to defend against.
    """
    return hashlib.sha256(raw_token.encode()).hexdigest()


def issue_invite(
    db: Session,
    *,
    email: str,
    role: RoleName,
    created_by: str | None,
    ttl: timedelta = DEFAULT_INVITE_TTL,
) -> tuple[UserInvite, str]:
    """Create an invite. Returns the row and the raw token, which is shown once.

    Re-inviting the same email is allowed and simply supersedes: any earlier
    unused invite is revoked, so a link that was shared to the wrong person
    stops working the moment a new one is issued.
    """
    normalised = email.strip().lower()

    role_row = db.scalar(select(Role).where(Role.name == role.value))
    if role_row is None:
        raise InviteError(f"Role {role.value} does not exist. Has the seed been run?")

    now = datetime.now(UTC)
    for stale in db.scalars(
        select(UserInvite).where(
            UserInvite.email == normalised,
            UserInvite.accepted_at.is_(None),
            UserInvite.revoked_at.is_(None),
        )
    ):
        stale.revoked_at = now

    raw_token = secrets.token_urlsafe(_TOKEN_BYTES)
    invite = UserInvite(
        email=normalised,
        role_id=role_row.id,
        token_hash=_hash_token(raw_token),
        expires_at=now + ttl,
        created_by=created_by,
    )
    db.add(invite)
    db.flush()
    return invite, raw_token


def validate_invite(db: Session, raw_token: str) -> UserInvite:
    """Return the invite this token unlocks, or raise InviteError.

    Every rejection says the same thing. Distinguishing "expired" from "already
    used" from "never existed" would confirm to a stranger which tokens are real.
    """
    invite = db.scalar(select(UserInvite).where(UserInvite.token_hash == _hash_token(raw_token)))

    if invite is None or invite.revoked_at is not None or invite.accepted_at is not None:
        raise InviteError("This invitation is no longer valid. Ask an admin for a new one.")

    # Stored tz-aware, but SQLite hands back naive datetimes; normalise so the
    # comparison cannot raise instead of rejecting.
    expires_at = invite.expires_at
    if expires_at.tzinfo is None:
        expires_at = expires_at.replace(tzinfo=UTC)

    if expires_at <= datetime.now(UTC):
        raise InviteError("This invitation is no longer valid. Ask an admin for a new one.")

    return invite


def accept_invite(db: Session, *, raw_token: str, user_id: str) -> UserInvite:
    """Redeem an invite for a freshly created user and attach the role.

    Called after the frontend has created the Better Auth user, so `user_id` is
    a real id. Marks the invite used in the same transaction as the grant: a
    half-applied invite would either burn a link with no role, or leave a link
    live after it had already been used.
    """
    invite = validate_invite(db, raw_token)

    invite.accepted_at = datetime.now(UTC)
    invite.accepted_user_id = user_id

    already_granted = db.scalar(
        select(UserRole).where(UserRole.user_id == user_id, UserRole.role_id == invite.role_id)
    )
    if already_granted is None:
        db.add(UserRole(user_id=user_id, role_id=invite.role_id, assigned_by=invite.created_by))

    # Without this the grant is invisible to any token already minted for them.
    bump_permissions_version(db, user_id)

    db.flush()
    return invite


def revoke_invite(db: Session, invite_id: uuid.UUID) -> None:
    """Cancel an unused invite, so its link stops working."""
    invite = db.get(UserInvite, invite_id)
    if invite is None or invite.accepted_at is not None:
        raise InviteError("No pending invitation to revoke.")
    invite.revoked_at = datetime.now(UTC)
    db.flush()
