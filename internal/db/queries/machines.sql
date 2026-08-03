-- The physical printer fleet (table: machines), distinct from machine_profiles
-- (the printer model/slicing profile - see config.sql). Query names are
-- Fleet-prefixed so they never collide with config.sql's machine_profiles
-- queries (ListMachineOps, GetMachineOps, ...) in the generated Queries struct.

-- name: ListFleetMachines :many
SELECT * FROM machines ORDER BY machine_id;

-- name: GetFleetMachine :one
SELECT * FROM machines WHERE id = sqlc.arg('id');

-- name: InsertFleetMachine :one
INSERT INTO machines (
    id, machine_id, name, image_url, status, filaments
) VALUES (
    sqlc.arg('id'), sqlc.arg('machine_id'), sqlc.arg('name'), sqlc.narg('image_url'),
    sqlc.arg('status'), sqlc.arg('filaments')
)
RETURNING *;

-- name: UpdateFleetMachineState :one
-- Sets what a machine is currently doing: idle/running/off, which batch (if
-- any), progress through it, and when the current print started (so a live
-- countdown can be computed as total_time - (now() - print_started_at)
-- instead of trusting a stored "remaining" number that nothing ticks down).
UPDATE machines SET
    status                   = sqlc.arg('status'),
    current_batch_id         = sqlc.narg('current_batch_id'),
    current_layer            = sqlc.narg('current_layer'),
    total_layers              = sqlc.narg('total_layers'),
    batch_total_time_minutes = sqlc.narg('batch_total_time_minutes'),
    print_started_at         = sqlc.narg('print_started_at'),
    updated_at                = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: UpdateFleetMachineFilaments :one
UPDATE machines SET
    filaments  = sqlc.arg('filaments'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: AdjustFleetMachineWaste :exec
-- Applies a signed delta (grams) to a machine's running waste total.
UPDATE machines SET
    total_waste_grams = total_waste_grams + sqlc.arg('delta')::float8,
    updated_at         = now()
WHERE id = sqlc.arg('id');
