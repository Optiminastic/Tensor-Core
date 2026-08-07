package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// InviteRequest is the input to IssueInvite: who to invite, as what role, which
// brands they may access, and by whom. Grouped so the signature stays small.
type InviteRequest struct {
	Email      string
	Role       RoleName
	BrandSlugs []string
	CreatedBy  string
	TTL        time.Duration
}

// IssueInvite creates an invite for an email and role, superseding any pending
// invite for that email. The brand slugs are stored on the invite and granted to
// the user on acceptance. It returns the stored row and the one-time raw token
// (never persisted). Run inside a transaction so the revoke and insert are atomic.
func IssueInvite(ctx context.Context, q *gen.Queries, req InviteRequest) (gen.UserInvite, string, error) {
	normalised := strings.ToLower(strings.TrimSpace(req.Email))

	roleID, err := q.GetRoleIDByName(ctx, string(req.Role))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.UserInvite{}, "", InviteError{
				Msg: fmt.Sprintf("Role %s does not exist. Has the seed been run?", req.Role),
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

	brandSlugsJSON, err := marshalBrandSlugs(req.BrandSlugs)
	if err != nil {
		return gen.UserInvite{}, "", err
	}

	createdBy := req.CreatedBy
	invite, err := q.InsertInvite(ctx, gen.InsertInviteParams{
		ID:         uuid.New(),
		Email:      normalised,
		RoleID:     roleID,
		TokenHash:  hashToken(raw),
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().UTC().Add(req.TTL), Valid: true},
		CreatedBy:  &createdBy,
		BrandSlugs: brandSlugsJSON,
	})
	if err != nil {
		return gen.UserInvite{}, "", err
	}
	return invite, raw, nil
}

// marshalBrandSlugs encodes the slug list as a jsonb array, always non-nil so the
// column's NOT NULL default is never relied upon accidentally.
func marshalBrandSlugs(slugs []string) ([]byte, error) {
	if slugs == nil {
		slugs = []string{}
	}
	return json.Marshal(slugs)
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
	// Grant the brands the admin assigned on the invite.
	var slugs []string
	if err := json.Unmarshal(invite.BrandSlugs, &slugs); err != nil {
		return gen.UserInvite{}, err
	}
	assignedBy := ""
	if invite.CreatedBy != nil {
		assignedBy = *invite.CreatedBy
	}
	for _, slug := range slugs {
		if err := q.InsertUserBrand(ctx, gen.InsertUserBrandParams{
			UserID: userID, BrandSlug: slug, AssignedBy: assignedBy,
		}); err != nil {
			return gen.UserInvite{}, err
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
