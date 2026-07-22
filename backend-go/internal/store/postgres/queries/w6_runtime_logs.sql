-- name: GetRuntimeLogDetail :one
SELECT
  rl.id,
  rl.time,
  rl.level,
  rl.trace_id,
  rl.event,
  rl.message,
  rl.error_message,
  rl.created_at,
  rl.raw_json
FROM juhe_dataset.runtime_logs AS rl
WHERE rl.id = sqlc.arg(id)::text
LIMIT 1;
