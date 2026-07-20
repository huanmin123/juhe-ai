-- First-pass account test session/task contract. The Go worker is intentionally
-- not part of this migration; these queries operate on the existing Node tables.
-- name: CreateManagementAccountTestSession :one
INSERT INTO juhe_business.account_test_sessions (id, request_system_account_id, request_role, request_system_account_filter_id, status, last_heartbeat_at, created_at, updated_at)
VALUES (sqlc.arg(id)::text, sqlc.arg(actor_id)::text, sqlc.arg(role)::text, NULLIF(sqlc.arg(filter_id)::text, ''), 'running', now(), now(), now())
RETURNING id, status, cancel_reason, last_heartbeat_at, cancel_requested_at, finished_at, created_at, updated_at;
