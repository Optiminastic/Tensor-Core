package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// InviteError is a message safe to show the invitee. Every rejection reason
// shares one message so a stranger cannot tell an expired token from a used one
// from one that never existed.
type InviteError struct{ Msg string }

func (e InviteError) Error() string { return e.Msg }

const (
	inviteTokenBytes = 32
	// DefaultInviteTTL is how long a fresh invite stays valid.
	DefaultInviteTTL = 72 * time.Hour

	invalidInviteMessage = "This invitation is no longer valid. Ask an admin for a new one."
	noPendingInviteMsg   = "No pending invitation to revoke."
)

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newRawToken() (string, error) {
	b := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueInvite creates an invite for an email and role, superseding any pending
// invite for that email. It returns the stored row and the one-time raw token
// (never persisted). Run inside a transaction so the revoke and insert are
// atomic.
//
// brandSlugs is what the invite PROMISES, not a grant: the person has no user
// id until they accept, so there is nothing to grant access to yet. It is
// stored on the invite row and applied by AcceptInvite. Before this it was
// accepted by the form, posted, and silently dropped.
func IssueInvite(
	ctx context.Context, q *gen.Queries, email string, role RoleName, createdBy string,
	ttl time.Duration, brandSlugs []string,
) (gen.UserInvite, string, error) {
	normalised := strings.ToLower(strings.TrimSpace(email))

	roleID, err := q.GetRoleIDByName(ctx, string(role))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.UserInvite{}, "", InviteError{
				Msg: fmt.Sprintf("Role %s does not exist. Has the seed been run?", role),
			}
		}
		return gen.UserInvite{}, "", err
	}

	if err := q.RevokePendingInvitesForEmail(ctx, normalised); err != nil {
		return gen.UserInvite{}, "", err
	}

	raw, err := newRawToken()
	if err != nil {
		return gen.UserInvite{}, "", err
	}

	createdByPtr := &createdBy
	invite, err := q.InsertInvite(ctx, gen.InsertInviteParams{
		ID:        uuid.New(),
		Email:     normalised,
		RoleID:    roleID,
		TokenHash: hashToken(raw),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(ttl), Valid: true},
		CreatedBy: createdByPtr,
		// Never nil: the column is NOT NULL, and an empty list is the ordinary
		// case of an invite that promises no brand in particular.
		BrandSlugs: append([]string{}, brandSlugs...),
	})
	if err != nil {
		return gen.UserInvite{}, "", err
	}
	return invite, raw, nil
}

// ValidateInvite looks up a live invite by its raw token. Missing, revoked,
// accepted and expired all yield the same InviteError message.
func ValidateInvite(ctx context.Context, q *gen.Queries, rawToken string) (gen.UserInvite, error) {
	invite, err := q.GetInviteByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.UserInvite{}, InviteError{Msg: invalidInviteMessage}
		}
		return gen.UserInvite{}, err
	}
	if invite.RevokedAt.Valid || invite.AcceptedAt.Valid {
		return gen.UserInvite{}, InviteError{Msg: invalidInviteMessage}
	}
	if !time.Now().UTC().Before(invite.ExpiresAt.Time) {
		return gen.UserInvite{}, InviteError{Msg: invalidInviteMessage}
	}
	return invite, nil
}

// AcceptInvite redeems an invite for a newly created user and attaches the role,
// bumping the user's permissions version. It is one-shot: a used or expired
// invite is rejected. Run inside a transaction.
func AcceptInvite(ctx context.Context, q *gen.Queries, rawToken, userID string) (gen.UserInvite, error) {
	invite, err := ValidateInvite(ctx, q, rawToken)
	if err != nil {
		return gen.UserInvite{}, err
	}
	if err := q.MarkInviteAccepted(ctx, gen.MarkInviteAcceptedParams{
		ID: invite.ID, AcceptedUserID: &userID,
	}); err != nil {
		return gen.UserInvite{}, err
	}
	if err := q.InsertUserRole(ctx, gen.InsertUserRoleParams{
		UserID: userID, RoleID: invite.RoleID, AssignedBy: invite.CreatedBy,
	}); err != nil {
		return gen.UserInvite{}, err
	}
	// The brands the invite promised, now that there is a user id to grant them
	// to. Best-effort PER SLUG: a brand deleted between the invite and the
	// acceptance must not fail the acceptance, because somebody is standing at
	// a password form and the alternative is an account that cannot be created.
	// They are told nothing; an admin can see and fix the brand list on the
	// roster, which is where brand access is managed from anyway.
	for _, slug := range invite.BrandSlugs {
		if err := q.InsertUserBrandAccess(ctx, gen.InsertUserBrandAccessParams{
			UserID: userID, BrandSlug: slug, GrantedBy: invite.CreatedBy,
		}); err != nil {
			continue
		}
	}
	if _, err := q.BumpPermissionsVersion(ctx, userID); err != nil {
		return gen.UserInvite{}, err
	}
	return invite, nil
}

// RevokeInvite kills a pending invite. A missing or already-accepted invite is
// an error; anything else is revoked.
func RevokeInvite(ctx context.Context, q *gen.Queries, inviteID uuid.UUID) error {
	invite, err := q.GetInviteByID(ctx, inviteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InviteError{Msg: noPendingInviteMsg}
		}
		return err
	}
	if invite.AcceptedAt.Valid {
		return InviteError{Msg: noPendingInviteMsg}
	}
	return q.MarkInviteRevoked(ctx, inviteID)
}
