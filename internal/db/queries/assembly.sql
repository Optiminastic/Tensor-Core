-- Multi-part products: a physical product unit (assembly_groups) and its part
-- slots (assembly_group_parts). A slot owns a stable part_uid and points at its
-- current print attempt via job_id, repointed on reprint.

-- name: InsertAssemblyGroup :one
INSERT INTO assembly_groups (id, order_id, design_sku, unit_index, status)
VALUES (sqlc.arg('id'), sqlc.narg('order_id'), sqlc.narg('design_sku'),
        sqlc.arg('unit_index'), sqlc.arg('status'))
RETURNING id, order_id, design_sku, unit_index, status, created_at, updated_at;

-- name: GetAssemblyGroup :one
SELECT id, order_id, design_sku, unit_index, status, created_at, updated_at
FROM assembly_groups WHERE id = $1;

-- name: SetAssemblyGroupStatus :one
UPDATE assembly_groups SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, order_id, design_sku, unit_index, status, created_at, updated_at;

-- name: ListAssemblyGroupsForOrder :many
SELECT id, order_id, design_sku, unit_index, status, created_at, updated_at
FROM assembly_groups WHERE order_id = $1 ORDER BY unit_index ASC, id ASC;

-- name: InsertAssemblyGroupPart :one
INSERT INTO assembly_group_parts (
    id, assembly_group_id, job_id, part_role, part_instance, part_uid
) VALUES (
    sqlc.arg('id'), sqlc.arg('assembly_group_id'), sqlc.narg('job_id'),
    sqlc.arg('part_role'), sqlc.arg('part_instance'), sqlc.arg('part_uid')
)
RETURNING id, assembly_group_id, job_id, part_role, part_instance, part_uid,
          created_at, updated_at;

-- name: ListAssemblyGroupParts :many
SELECT id, assembly_group_id, job_id, part_role, part_instance, part_uid,
       created_at, updated_at
FROM assembly_group_parts
WHERE assembly_group_id = $1
ORDER BY part_role ASC, part_instance ASC;

-- name: GetAssemblyPartByJob :one
-- The slot a job currently fills, or no rows for a non-part (single-product) job.
SELECT id, assembly_group_id, job_id, part_role, part_instance, part_uid,
       created_at, updated_at
FROM assembly_group_parts WHERE job_id = $1;

-- name: RepointAssemblyPartJob :exec
-- Move a slot to a new print attempt (used when a failed part is reprinted). The
-- slot, and its part_uid, are unchanged - only the current job_id moves.
UPDATE assembly_group_parts SET job_id = sqlc.arg('job_id'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: GetAssemblyGroupReadiness :one
-- For gating: how many slots the group has, and how many are currently built
-- (their current job is completed and QC-passed). ready = total means the unit
-- can be assembled.
SELECT
    count(*)::int AS total_parts,
    count(*) FILTER (
        WHERE j.status = 'completed' AND j.qc_status = 'passed'
    )::int AS ready_parts
FROM assembly_group_parts p
LEFT JOIN production_jobs j ON j.id = p.job_id
WHERE p.assembly_group_id = $1;
