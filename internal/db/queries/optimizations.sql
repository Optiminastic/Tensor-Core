-- name: GetDesignOptimization :one
SELECT id, design_id, input_hash, result, model, created_at
FROM design_optimizations
WHERE design_id = sqlc.arg('design_id') AND input_hash = sqlc.arg('input_hash');

-- name: UpsertDesignOptimization :one
INSERT INTO design_optimizations (id, design_id, input_hash, result, model)
VALUES (sqlc.arg('id'), sqlc.arg('design_id'), sqlc.arg('input_hash'), sqlc.arg('result'), sqlc.arg('model'))
ON CONFLICT (design_id, input_hash)
DO UPDATE SET result = EXCLUDED.result, model = EXCLUDED.model, created_at = now()
RETURNING id, design_id, input_hash, result, model, created_at;
