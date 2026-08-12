-- The job history. Append-only by construction: there is deliberately no UPDATE
-- and no DELETE here, and a database trigger enforces it (migration 0035).

-- name: InsertJobEvent :one
INSERT INTO production_job_events (
    id, job_id, event_type, stage, reason, comment, actor_id, batch_id, related_job_id, metadata
) VALUES (
    sqlc.arg('id'), sqlc.arg('job_id'), sqlc.arg('event_type'), sqlc.narg('stage'),
    sqlc.narg('reason'), sqlc.narg('comment'), sqlc.arg('actor_id'), sqlc.narg('batch_id'),
    sqlc.narg('related_job_id'), sqlc.arg('metadata')
)
RETURNING id, job_id, seq, event_type, stage, reason, comment, actor_id, batch_id,
          related_job_id, metadata, created_at;

-- name: ListJobEvents :many
-- One job's history, oldest first - a timeline reads downward.
SELECT id, job_id, seq, event_type, stage, reason, comment, actor_id, batch_id,
       related_job_id, metadata, created_at
FROM production_job_events
WHERE job_id = sqlc.arg('job_id')
ORDER BY seq ASC;

-- name: CountOpenIssuesForJobs :many
-- Which jobs have an unresolved issue, for the station queues' warning pill.
-- An issue is open until that same stage subsequently completes: raising one
-- does not block progress (the spec is explicit that the product must stay
-- visible), so "resolved" can only mean "the stage moved on afterwards".
SELECT e.job_id, e.stage, count(*)::int AS open_count,
       (array_agg(e.reason ORDER BY e.seq DESC))[1] AS latest_reason
FROM production_job_events e
WHERE e.job_id = ANY(sqlc.arg('job_ids')::uuid[])
  AND e.event_type LIKE '%.issue'
  AND NOT EXISTS (
        SELECT 1 FROM production_job_events c
        WHERE c.job_id = e.job_id
          AND c.stage = e.stage
          AND c.event_type NOT LIKE '%.issue'
          AND c.seq > e.seq)
GROUP BY e.job_id, e.stage;
