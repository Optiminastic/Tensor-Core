-- Multi-part products: a design's named parts (its recipe). A design with no rows
-- here is a single-part product and follows the legacy one-job-per-line path.

-- name: InsertDesignPart :one
INSERT INTO design_parts (
    id, design_id, role, part_index, quantity, print_file_id, material, colour, nozzle_profile
) VALUES (
    sqlc.arg('id'), sqlc.arg('design_id'), sqlc.arg('role'), sqlc.arg('part_index'),
    sqlc.arg('quantity'), sqlc.narg('print_file_id'), sqlc.narg('material'),
    sqlc.narg('colour'), sqlc.narg('nozzle_profile')
)
RETURNING id, design_id, role, part_index, quantity, print_file_id, material, colour,
          nozzle_profile, created_at;

-- name: ListDesignPartsByDesign :many
-- The parts of a design, in declared order. Empty for a single-part product.
SELECT id, design_id, role, part_index, quantity, print_file_id, material, colour,
       nozzle_profile, created_at
FROM design_parts
WHERE design_id = $1
ORDER BY part_index ASC, role ASC;

-- name: GetDesignPart :one
SELECT id, design_id, role, part_index, quantity, print_file_id, material, colour,
       nozzle_profile, created_at
FROM design_parts WHERE id = $1;

-- name: UpdateDesignPart :one
UPDATE design_parts SET
    role           = COALESCE(sqlc.narg('role'), role),
    part_index     = COALESCE(sqlc.narg('part_index'), part_index),
    quantity       = COALESCE(sqlc.narg('quantity'), quantity),
    print_file_id  = CASE WHEN sqlc.arg('set_print_file')::bool THEN sqlc.narg('print_file_id') ELSE print_file_id END,
    material       = COALESCE(sqlc.narg('material'), material),
    colour         = COALESCE(sqlc.narg('colour'), colour),
    nozzle_profile = COALESCE(sqlc.narg('nozzle_profile'), nozzle_profile)
WHERE id = sqlc.arg('id')
RETURNING id, design_id, role, part_index, quantity, print_file_id, material, colour,
          nozzle_profile, created_at;

-- name: DeleteDesignPart :exec
DELETE FROM design_parts WHERE id = $1;

-- name: CountDesignParts :one
SELECT count(*) FROM design_parts WHERE design_id = $1;
