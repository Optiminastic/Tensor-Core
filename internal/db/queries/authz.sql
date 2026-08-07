-- Roles, permissions, grants and per-user authz state.

-- name: GetUserRoleGrants :many
-- One row per (role, permission). resource/action are NULL for a role with no
-- grants (left join), so a user with a granted-but-empty role still appears.
SELECT r.name AS role_name,
       p.resource AS resource,
       p.action AS action
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p ON p.id = rp.permission_id
WHERE ur.user_id = $1;

-- name: GetPermissionsVersion :one
SELECT permissions_version FROM user_authz_state WHERE user_id = $1;

-- name: BumpPermissionsVersion :one
INSERT INTO user_authz_state (user_id, permissions_version)
VALUES ($1, 1)
ON CONFLICT (user_id)
DO UPDATE SET permissions_version = user_authz_state.permissions_version + 1,
              updated_at = now()
RETURNING permissions_version;

-- name: UpsertPermission :one
INSERT INTO permissions (id, resource, action, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (resource, action)
DO UPDATE SET description = EXCLUDED.description, updated_at = now()
RETURNING id;

-- name: UpsertRole :one
INSERT INTO roles (id, name, description)
VALUES ($1, $2, $3)
ON CONFLICT (name)
DO UPDATE SET description = EXCLUDED.description, updated_at = now()
RETURNING id;

-- name: GetRoleIDByName :one
SELECT id FROM roles WHERE name = $1;

-- name: GetRoleNameByID :one
SELECT name::text FROM roles WHERE id = $1;

-- name: GetPermissionID :one
SELECT id FROM permissions WHERE resource = $1 AND action = $2;

-- name: ListRolePermissionIDs :many
SELECT permission_id FROM role_permissions WHERE role_id = $1;

-- name: InsertRolePermission :exec
INSERT INTO role_permissions (role_id, permission_id)
VALUES ($1, $2)
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- name: DeleteRolePermission :exec
DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = $2;

-- name: AdminExists :one
SELECT EXISTS (
    SELECT 1 FROM user_roles ur
    JOIN roles r ON r.id = ur.role_id
    WHERE r.name = 'ADMIN'
) AS admin_exists;

-- name: InsertUserRole :exec
INSERT INTO user_roles (user_id, role_id, assigned_by)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, role_id) DO NOTHING;

-- name: ListMembers :many
-- One row per user that has at least one role, with their role names aggregated.
-- Emails are resolved on the frontend (Better Auth owns the user table).
SELECT ur.user_id,
       array_agg(r.name::text ORDER BY r.name)::text[] AS roles
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
GROUP BY ur.user_id
ORDER BY ur.user_id;

-- name: DeleteUserRoles :exec
-- Remove every role from a user (de-provision them). Their Better Auth account
-- stays; with no roles they fail closed and can do nothing.
DELETE FROM user_roles WHERE user_id = $1;

-- name: CountAdminsExcluding :one
-- How many OTHER users hold the ADMIN role, so the last admin cannot be removed.
SELECT count(DISTINCT ur.user_id)::int AS other_admins
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE r.name = 'ADMIN' AND ur.user_id <> $1;
