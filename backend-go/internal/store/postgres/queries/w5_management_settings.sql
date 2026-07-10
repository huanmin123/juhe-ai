-- name: LockManagementGlobalSettings :many
SELECT key, value_json
FROM juhe_business.global_settings
WHERE key IN ('appName', 'appIcon')
ORDER BY key ASC
FOR UPDATE;

-- name: UpdateManagementGlobalSetting :one
UPDATE juhe_business.global_settings
SET
  value_json = sqlc.arg(value_json)::text,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE key = sqlc.arg(key)::text
RETURNING key, value_json;
