-- name: FindPublicAPIKeyTargetByUsername :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE lower(username) = lower($1)
LIMIT 1;

-- name: FindPublicAPIKeyTargetByID :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1;

-- name: FindPublicAPIKeyRouteStrategy :one
SELECT id, system_account_id, name, mode, status
FROM juhe_business.route_strategies
WHERE system_account_id = sqlc.arg(system_account_id)
  AND id = sqlc.arg(route_strategy_id)
LIMIT 1;

-- name: FindPublicAPIKeyRouteStrategyForUpdate :one
SELECT id, system_account_id, name, mode, status
FROM juhe_business.route_strategies
WHERE system_account_id = sqlc.arg(system_account_id)
  AND id = sqlc.arg(route_strategy_id)
LIMIT 1
FOR UPDATE;

-- name: ListPublicAPIKeys :many
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.name,
  api_keys.description,
  api_keys.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  api_keys.status,
  api_keys.is_default,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  api_keys.availability_schedule_json,
  api_keys.availability_schedule_next_check_at,
  api_keys.last_used_at,
  api_keys.created_at,
  api_keys.updated_at
FROM juhe_business.api_keys AS api_keys
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE api_keys.system_account_id = sqlc.arg(system_account_id)
  AND (sqlc.arg(route_strategy_id)::text = '' OR api_keys.route_strategy_id = sqlc.arg(route_strategy_id))
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (api_keys.name COLLATE "C" >= sqlc.arg(keyword)::text AND api_keys.name COLLATE "C" < sqlc.arg(keyword_upper)::text)
  )
  AND (sqlc.arg(status)::text = '' OR api_keys.status = sqlc.arg(status))
ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: FindPublicAPIKeyByID :one
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.name,
  api_keys.description,
  api_keys.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  api_keys.status,
  api_keys.is_default,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  api_keys.availability_schedule_json,
  api_keys.availability_schedule_next_check_at,
  api_keys.last_used_at,
  api_keys.created_at,
  api_keys.updated_at
FROM juhe_business.api_keys AS api_keys
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE api_keys.id = $1
LIMIT 1;

-- name: FindPublicAPIKeyByIDForUpdate :one
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.name,
  api_keys.description,
  api_keys.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  api_keys.status,
  api_keys.is_default,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  api_keys.availability_schedule_json,
  api_keys.availability_schedule_next_check_at,
  api_keys.last_used_at,
  api_keys.created_at,
  api_keys.updated_at
FROM juhe_business.api_keys AS api_keys
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE api_keys.id = $1
LIMIT 1
FOR UPDATE OF api_keys;

-- name: InsertPublicAPIKey :one
WITH inserted AS (
  INSERT INTO juhe_business.api_keys (
    id,
    system_account_id,
    route_strategy_id,
    name,
    description,
    key_hash,
    key_prefix,
    key_suffix,
    status,
    is_default,
    expires_at,
    quota_limits_json,
    availability_schedule_json,
    availability_schedule_next_check_at,
    created_at,
    updated_at
  ) VALUES (
    sqlc.arg(id),
    sqlc.arg(system_account_id),
    sqlc.arg(route_strategy_id),
    sqlc.arg(name),
    sqlc.arg(description),
    sqlc.arg(key_hash),
    sqlc.arg(key_prefix),
    sqlc.arg(key_suffix),
    sqlc.arg(status),
    false,
    sqlc.arg(expires_at),
    sqlc.arg(quota_limits_json),
    sqlc.arg(availability_schedule_json),
    sqlc.arg(availability_schedule_next_check_at),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
  )
  RETURNING
    id,
    system_account_id,
    route_strategy_id,
    name,
    description,
    key_hash,
    key_prefix,
    key_suffix,
    status,
    is_default,
    expires_at,
    quota_limits_json,
    availability_schedule_json,
    availability_schedule_next_check_at,
    last_used_at,
    created_at,
    updated_at
)
SELECT
  inserted.id,
  inserted.system_account_id,
  inserted.name,
  inserted.description,
  inserted.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  inserted.status,
  inserted.is_default,
  inserted.key_prefix,
  inserted.key_suffix,
  inserted.expires_at,
  inserted.quota_limits_json,
  inserted.availability_schedule_json,
  inserted.availability_schedule_next_check_at,
  inserted.last_used_at,
  inserted.created_at,
  inserted.updated_at
FROM inserted
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = inserted.route_strategy_id
  AND route_strategies.system_account_id = inserted.system_account_id;

-- name: UpdatePublicAPIKeyAllFields :one
WITH updated AS (
  UPDATE juhe_business.api_keys AS api_keys
  SET route_strategy_id = sqlc.arg(route_strategy_id),
      name = sqlc.arg(name),
      description = sqlc.arg(description),
      status = sqlc.arg(status),
      expires_at = sqlc.arg(expires_at),
      quota_limits_json = sqlc.arg(quota_limits_json),
      availability_schedule_json = sqlc.arg(availability_schedule_json),
      availability_schedule_next_check_at = sqlc.arg(availability_schedule_next_check_at),
      updated_at = sqlc.arg(updated_at)
  WHERE api_keys.id = sqlc.arg(id)
    AND api_keys.system_account_id = sqlc.arg(system_account_id)
  RETURNING
    id,
    system_account_id,
    route_strategy_id,
    name,
    description,
    key_hash,
    key_prefix,
    key_suffix,
    status,
    is_default,
    expires_at,
    quota_limits_json,
    availability_schedule_json,
    availability_schedule_next_check_at,
    last_used_at,
    created_at,
    updated_at
)
SELECT
  updated.id,
  updated.system_account_id,
  updated.name,
  updated.description,
  updated.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  updated.status,
  updated.is_default,
  updated.key_prefix,
  updated.key_suffix,
  updated.expires_at,
  updated.quota_limits_json,
  updated.availability_schedule_json,
  updated.availability_schedule_next_check_at,
  updated.last_used_at,
  updated.created_at,
  updated.updated_at
FROM updated
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = updated.route_strategy_id
  AND route_strategies.system_account_id = updated.system_account_id;

-- name: DeletePublicAPIKey :execrows
DELETE FROM juhe_business.api_keys
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: UpsertPublicAPIKeyRecordCleanupTarget :exec
INSERT INTO juhe_dataset.api_key_record_cleanup_targets (
  api_key_id,
  system_account_id,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(api_key_id),
  sqlc.arg(system_account_id),
  sqlc.arg(created_at),
  sqlc.arg(updated_at)
)
ON CONFLICT (api_key_id) DO UPDATE SET
  system_account_id = EXCLUDED.system_account_id,
  updated_at = EXCLUDED.updated_at;
