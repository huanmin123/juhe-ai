-- name: InsertOperationLog :one
INSERT INTO juhe_dataset.operation_logs (
  id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
  operation_scope_system_account_id, mode, module, action, operation_key, resource_type,
  resource_id, resource_name, summary, detail_level, visibility_scope, changes_json,
  metadata_json, method, path, status_code, client_ip, user_agent, created_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.narg(trace_id)::text,
  sqlc.arg(actor_system_account_id)::text,
  sqlc.narg(actor_username)::text,
  sqlc.narg(actor_display_name)::text,
  sqlc.arg(actor_role)::text,
  sqlc.narg(operation_scope_system_account_id)::text,
  sqlc.arg(mode)::text,
  sqlc.arg(module)::text,
  sqlc.arg(action)::text,
  sqlc.arg(operation_key)::text,
  sqlc.arg(resource_type)::text,
  sqlc.narg(resource_id)::text,
  sqlc.narg(resource_name)::text,
  sqlc.arg(summary)::text,
  sqlc.arg(detail_level)::text,
  sqlc.arg(visibility_scope)::text,
  sqlc.arg(changes_json)::text,
  sqlc.arg(metadata_json)::text,
  sqlc.narg(method)::text,
  sqlc.narg(path)::text,
  sqlc.narg(status_code)::integer,
  sqlc.narg(client_ip)::text,
  sqlc.narg(user_agent)::text,
  sqlc.arg(created_at)::timestamptz
)
ON CONFLICT (id) DO NOTHING
RETURNING id;

-- name: InsertOperationLogTarget :exec
INSERT INTO juhe_dataset.operation_log_targets (
  id, operation_log_id, target_type, target_id, target_name,
  target_owner_system_account_id, relation, created_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(operation_log_id)::text,
  sqlc.arg(target_type)::text,
  sqlc.narg(target_id)::text,
  sqlc.narg(target_name)::text,
  sqlc.narg(target_owner_system_account_id)::text,
  sqlc.arg(relation)::text,
  sqlc.arg(created_at)::timestamptz
);

-- name: InsertOperationLogViewer :exec
INSERT INTO juhe_dataset.operation_log_viewers (
  operation_log_id, system_account_id, visibility_reason, detail_level, created_at
) VALUES (
  sqlc.arg(operation_log_id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(visibility_reason)::text,
  sqlc.arg(detail_level)::text,
  sqlc.arg(created_at)::timestamptz
)
ON CONFLICT (operation_log_id, system_account_id, visibility_reason) DO NOTHING;

-- name: InsertOperationLogSearchTerms :exec
INSERT INTO juhe_dataset.operation_log_summary_search_terms (
  operation_log_id, term, created_at
)
SELECT
  sqlc.arg(operation_log_id)::text,
  term,
  sqlc.arg(created_at)::timestamptz
FROM unnest(sqlc.arg(terms)::text[]) AS term
WHERE term <> ''
ON CONFLICT (term, operation_log_id) DO NOTHING;
