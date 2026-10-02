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
-- Every user who holds a role, with their roles.
--
-- One query rather than a roster read plus one per member: the People page
-- shows every member with their roles, and doing it per row is how a page that
-- renders fine with four people stops loading at forty.
--
-- No brands. Store-level access was removed - every role reaches every brand -
-- so there is nothing per-member to report (migration 0087).
--
-- DISTINCT inside the aggregate because a user with two roles produces two
-- rows, and array_remove drops the NULL a role with no grants would contribute.
SELECT ur.user_id,
       array_remove(array_agg(DISTINCT r.name), NULL)::text[] AS roles
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
GROUP BY ur.user_id
ORDER BY ur.user_id;

-- name: DeleteUserRoles :exec
-- Strip every role from a user. Their Better Auth account is untouched - this
-- is "no longer a member of this workspace", not "deleted".
DELETE FROM user_roles WHERE user_id = $1;

-- name: UserHasAnyRole :one
-- Whether this user is a member at all. The remove path checks it so deleting
-- somebody who was never a member is a 404 rather than a silent success.
SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id = $1) AS is_member;
