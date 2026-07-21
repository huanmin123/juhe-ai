-- name: GetManagementAccountTestSession :one
SELECT id, status, COALESCE(cancel_reason, ''), last_heartbeat_at, cancel_requested_at, finished_at, created_at, updated_at
FROM juhe_business.account_test_sessions
WHERE id = sqlc.arg(id)::text
  AND request_system_account_id = sqlc.arg(actor_id)::text
  AND COALESCE(request_system_account_filter_id, '') = sqlc.arg(filter_id)::text
LIMIT 1;

-- name: ListManagementAccountTestTasks :many
SELECT t.id
FROM juhe_business.account_test_tasks t
WHERE t.id = ANY(sqlc.arg(ids)::text[])
  AND t.request_system_account_id = sqlc.arg(actor_id)::text
  AND COALESCE(t.request_system_account_filter_id, '') = sqlc.arg(filter_id)::text
ORDER BY array_position(sqlc.arg(ids)::text[], t.id);
