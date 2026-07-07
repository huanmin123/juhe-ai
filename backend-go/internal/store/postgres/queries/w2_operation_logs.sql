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

-- name: ListOperationLogs :many
SELECT ol.*
FROM juhe_dataset.operation_logs AS ol
WHERE (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.arg(actor_system_account_id)::text = '' OR ol.actor_system_account_id = sqlc.arg(actor_system_account_id)::text)
  AND (sqlc.arg(operation_scope_system_account_id)::text = '' OR ol.operation_scope_system_account_id = sqlc.arg(operation_scope_system_account_id)::text)
  AND (
    sqlc.arg(affected_system_account_id)::text = ''
    OR ol.visibility_scope = 'all_users'
    OR EXISTS (
      SELECT 1
      FROM juhe_dataset.operation_log_viewers AS affected
      WHERE affected.operation_log_id = ol.id
        AND affected.system_account_id = sqlc.arg(affected_system_account_id)::text
    )
  )
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY ol.created_at DESC, ol.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListOperationLogsBySummarySearch :many
SELECT ol.*
FROM juhe_dataset.operation_log_summary_search_terms AS search
INNER JOIN juhe_dataset.operation_logs AS ol
  ON ol.id = search.operation_log_id
WHERE search.term = sqlc.arg(search_term)::text
  AND (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.arg(actor_system_account_id)::text = '' OR ol.actor_system_account_id = sqlc.arg(actor_system_account_id)::text)
  AND (sqlc.arg(operation_scope_system_account_id)::text = '' OR ol.operation_scope_system_account_id = sqlc.arg(operation_scope_system_account_id)::text)
  AND (
    sqlc.arg(affected_system_account_id)::text = ''
    OR ol.visibility_scope = 'all_users'
    OR EXISTS (
      SELECT 1
      FROM juhe_dataset.operation_log_viewers AS affected
      WHERE affected.operation_log_id = ol.id
        AND affected.system_account_id = sqlc.arg(affected_system_account_id)::text
    )
  )
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY search.created_at DESC, search.operation_log_id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListVisibleTargetedOperationLogs :many
SELECT ol.*
FROM juhe_dataset.operation_log_viewers AS visible
INNER JOIN juhe_dataset.operation_logs AS ol
  ON ol.id = visible.operation_log_id
WHERE visible.system_account_id = sqlc.arg(system_account_id)::text
  AND ol.visibility_scope = 'targeted'
  AND NOT EXISTS (
    SELECT 1
    FROM juhe_dataset.operation_log_viewers AS previous
    WHERE previous.operation_log_id = visible.operation_log_id
      AND previous.system_account_id = visible.system_account_id
      AND previous.visibility_reason < visible.visibility_reason
  )
  AND (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY visible.created_at DESC, visible.operation_log_id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListVisibleTargetedOperationLogsBySummarySearch :many
SELECT ol.*
FROM juhe_dataset.operation_log_summary_search_terms AS search
INNER JOIN juhe_dataset.operation_logs AS ol
  ON ol.id = search.operation_log_id
INNER JOIN juhe_dataset.operation_log_viewers AS visible
  ON visible.operation_log_id = ol.id
  AND visible.system_account_id = sqlc.arg(system_account_id)::text
WHERE search.term = sqlc.arg(search_term)::text
  AND ol.visibility_scope = 'targeted'
  AND NOT EXISTS (
    SELECT 1
    FROM juhe_dataset.operation_log_viewers AS previous
    WHERE previous.operation_log_id = visible.operation_log_id
      AND previous.system_account_id = visible.system_account_id
      AND previous.visibility_reason < visible.visibility_reason
  )
  AND (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY search.created_at DESC, search.operation_log_id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListVisibleAllUsersOperationLogs :many
SELECT ol.*
FROM juhe_dataset.operation_logs AS ol
WHERE ol.visibility_scope = 'all_users'
  AND (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY ol.created_at DESC, ol.id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListVisibleAllUsersOperationLogsBySummarySearch :many
SELECT ol.*
FROM juhe_dataset.operation_log_summary_search_terms AS search
INNER JOIN juhe_dataset.operation_logs AS ol
  ON ol.id = search.operation_log_id
WHERE search.term = sqlc.arg(search_term)::text
  AND ol.visibility_scope = 'all_users'
  AND (
    sqlc.arg(trace_id)::text = ''
    OR (
      ol.trace_id COLLATE "C" >= sqlc.arg(trace_id)::text
      AND ol.trace_id COLLATE "C" < sqlc.arg(trace_id_upper)::text
    )
  )
  AND (sqlc.arg(module)::text = '' OR ol.module = sqlc.arg(module)::text)
  AND (sqlc.arg(action)::text = '' OR ol.action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_type)::text = '' OR ol.resource_type = sqlc.arg(resource_type)::text)
  AND (sqlc.arg(resource_id)::text = '' OR ol.resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR ol.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR ol.created_at <= sqlc.narg(end_at)::timestamptz)
ORDER BY search.created_at DESC, search.operation_log_id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: GetOperationLogDetail :one
SELECT ol.*
FROM juhe_dataset.operation_logs AS ol
WHERE ol.id = sqlc.arg(id)::text
LIMIT 1;

-- name: GetVisibleOperationLogDetail :one
SELECT ol.*
FROM juhe_dataset.operation_logs AS ol
WHERE ol.id = sqlc.arg(id)::text
  AND (
    ol.visibility_scope = 'all_users'
    OR (
      ol.visibility_scope = 'targeted'
      AND EXISTS (
        SELECT 1
        FROM juhe_dataset.operation_log_viewers AS visible
        WHERE visible.operation_log_id = ol.id
          AND visible.system_account_id = sqlc.arg(system_account_id)::text
      )
    )
  )
LIMIT 1;

-- name: ListOperationLogTargets :many
SELECT *
FROM juhe_dataset.operation_log_targets
WHERE operation_log_id = sqlc.arg(operation_log_id)::text
ORDER BY created_at ASC, id ASC;

-- name: ListOperationLogViewers :many
SELECT *
FROM juhe_dataset.operation_log_viewers
WHERE operation_log_id = sqlc.arg(operation_log_id)::text
ORDER BY created_at ASC, system_account_id ASC;

-- name: ListOperationLogViewerDetailLevels :many
SELECT operation_log_id, detail_level
FROM juhe_dataset.operation_log_viewers
WHERE system_account_id = sqlc.arg(system_account_id)::text
  AND operation_log_id = ANY(sqlc.arg(operation_log_ids)::text[]);

-- name: GetOperationLogViewerDetailLevel :one
SELECT detail_level
FROM juhe_dataset.operation_log_viewers
WHERE operation_log_id = sqlc.arg(operation_log_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text
ORDER BY CASE WHEN detail_level = 'full' THEN 0 ELSE 1 END ASC
LIMIT 1;
