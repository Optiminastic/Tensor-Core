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

-- name: SetFleetMachineProfile :one
-- Links a physical unit to the slicing config it runs. Used by the seed step
-- and, later, by fleet admin tooling.
UPDATE machines SET machine_profile_id = sqlc.arg('machine_profile_id'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListFleetMachinesWithFamily :many
-- Every fleet machine with its linked profile's family and operational status
-- (both null if unlinked) - what the earliest-free-machine scheduler ranks
-- over for a given batch's required machine family, skipping any fleet unit
-- that's off or whose linked profile is offline/maintenance.
SELECT m.*, mp.family AS profile_family, mp.status AS profile_status
FROM machines m
LEFT JOIN machine_profiles mp ON mp.id = m.machine_profile_id
ORDER BY m.machine_id;

-- name: ListQueuedBatchesForFleetMachine :many
-- Batches already open/in_progress on this fleet machine's linked profile,
-- oldest first (FCFS) - both the scheduler's load calculation and the
-- GET /machine-fleet/:id/queue endpoint.
-- Order is the order these will actually print, so it is also the order the
-- machine's queue is shown in and the order startNextBatch picks from.
--
-- This used to be plain created_at, which quietly threw away every scheduling
-- decision made upstream: a batch full of urgent, due-today work queued behind
-- a routine one simply because the routine one was planned an hour earlier. All
-- the priority and due-date weighting in batch scoring stopped mattering the
-- moment the batch reached a machine.
--
-- Priority and due date live on the jobs, not the batch, so both are taken from
-- the bed's most pressing job - a plate prints as one unit and inherits the
-- tightest deadline on it.
--
-- in_progress sorts first unconditionally. That batch is physically on the bed;
-- its position is not a scheduling decision any more and re-ordering it would
-- only misreport what the machine is doing.
SELECT b.*
FROM batches b
JOIN machines m ON m.machine_profile_id = b.machine_id
LEFT JOIN LATERAL (
    SELECT min(j.priority) AS min_priority, min(j.due_date) AS earliest_due
    FROM production_jobs j
    WHERE j.batch_id = b.id
) urgency ON true
WHERE m.id = sqlc.arg('fleet_machine_id')
  AND b.status IN ('open', 'in_progress')
ORDER BY (b.status = 'in_progress') DESC,
         urgency.min_priority ASC NULLS LAST,
         urgency.earliest_due ASC NULLS LAST,
         b.created_at ASC, b.id ASC;

-- name: ListAllBatchesForFleetMachine :many
-- Every batch on this fleet machine's linked profile, newest first, whatever
-- its status - what the machine's own Kanban board renders.
--
-- Deliberately separate from ListQueuedBatchesForFleetMachine rather than a
-- widening of it. That query means "work still outstanding on this machine",
-- and two callers depend on exactly that meaning: the scheduler's load
-- calculation (queuedBatchLoad, which sums remaining print time to rank
-- machines) and computeLiveFilaments. Including completed batches there would
-- make every machine look permanently overloaded.
--
-- The board, by contrast, has Draft and Completed columns that were
-- structurally always empty, because its feed could only ever return 'open'
-- and 'in_progress'. LIMIT keeps a machine with a long history from returning
-- everything it has ever printed.
SELECT b.*
FROM batches b
JOIN machines m ON m.machine_profile_id = b.machine_id
WHERE m.id = sqlc.arg('fleet_machine_id')
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg('row_limit');
