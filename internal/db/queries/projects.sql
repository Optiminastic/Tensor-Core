-- Projects. brand is a free-form brand slug (text). status stays a fixed set.

-- name: ListProjects :many
SELECT id, name, brand, description,
       status::text AS status, created_by, created_at, updated_at
FROM projects ORDER BY name;

-- name: GetProject :one
SELECT id, name, brand, description,
       status::text AS status, created_by, created_at, updated_at
FROM projects WHERE id = $1;

-- name: InsertProject :one
INSERT INTO projects (id, name, brand, description, status, created_by)
VALUES (sqlc.arg('id'), sqlc.arg('name'), sqlc.arg('brand'),
        sqlc.narg('description'), sqlc.arg('status')::project_status, sqlc.arg('created_by'))
RETURNING id, name, brand, description,
          status::text AS status, created_by, created_at, updated_at;

-- name: UpdateProject :one
UPDATE projects SET
    name = COALESCE(sqlc.narg('name'), name),
    brand = COALESCE(sqlc.narg('brand'), brand),
    description = CASE WHEN sqlc.arg('set_description')::bool
                      THEN sqlc.narg('description') ELSE description END,
    status = COALESCE(sqlc.narg('status')::project_status, status),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, name, brand, description,
          status::text AS status, created_by, created_at, updated_at;
