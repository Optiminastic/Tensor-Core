-- User invites.

-- name: RevokePendingInvitesForEmail :exec
UPDATE user_invites
SET revoked_at = now(), updated_at = now()
WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: InsertInvite :one
INSERT INTO user_invites (id, email, role_id, token_hash, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, email, role_id, token_hash, expires_at,
          accepted_at, accepted_user_id, revoked_at, created_by, created_at, updated_at;

-- name: GetInviteByTokenHash :one
SELECT id, email, role_id, token_hash, expires_at,
       accepted_at, accepted_user_id, revoked_at, created_by, created_at, updated_at
FROM user_invites
WHERE token_hash = $1;

-- name: GetInviteByID :one
SELECT id, email, role_id, token_hash, expires_at,
       accepted_at, accepted_user_id, revoked_at, created_by, created_at, updated_at
FROM user_invites
WHERE id = $1;

-- name: MarkInviteAccepted :exec
UPDATE user_invites
SET accepted_at = now(), accepted_user_id = $2, updated_at = now()
WHERE id = $1;

-- name: MarkInviteRevoked :exec
UPDATE user_invites
SET revoked_at = now(), updated_at = now()
WHERE id = $1;

-- name: ListInvitesWithRole :many
-- Newest first. Joined to the role so the admin list shows the role name.
SELECT i.id, i.email, r.name::text AS role_name, i.expires_at,
       i.accepted_at, i.revoked_at, i.created_by
FROM user_invites i
JOIN roles r ON r.id = i.role_id
ORDER BY i.created_at DESC;

-- name: ListInvitesWithRolePage :many
-- Keyset page of the invite list, newest first, strictly before the (created_at,
-- id) cursor. A null cursor returns the first page. created_at is selected so the
-- caller can build the next cursor; it is not part of the response body.
SELECT i.id, i.email, r.name::text AS role_name, i.expires_at,
       i.accepted_at, i.revoked_at, i.created_by, i.created_at
FROM user_invites i
JOIN roles r ON r.id = i.role_id
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (i.created_at, i.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
ORDER BY i.created_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit');
