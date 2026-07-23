-- name: CreateManagementAPIKey :one
WITH route_target AS MATERIALIZED (
  SELECT
    route_strategies.id AS route_strategy_id,
    route_strategies.system_account_id,
    system_accounts.display_name AS system_account_name,
    route_strategies.name AS route_strategy_name,
    route_strategies.mode AS route_strategy_mode,
    route_strategies.status AS route_strategy_status
  FROM juhe_business.route_strategies AS route_strategies
  INNER JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = route_strategies.system_account_id
  WHERE route_strategies.id = sqlc.arg(route_strategy_id)::text
    AND route_strategies.system_account_id = sqlc.arg(system_account_id)::text
  FOR UPDATE OF route_strategies
),
inserted_api_key AS (
  INSERT INTO juhe_business.api_keys (
    id,
    system_account_id,
    route_strategy_id,
    name,
    description,
    key_hash,
    key_prefix,
    key_suffix,
    key_secret_encrypted,
    status,
    is_default,
    purpose,
    expires_at,
    quota_limits_json,
    availability_schedule_json,
    availability_schedule_next_check_at,
    created_at,
    updated_at
  )
  SELECT
    sqlc.arg(id)::text,
    route_target.system_account_id,
    route_target.route_strategy_id,
    sqlc.arg(name)::text,
    sqlc.narg(description)::text,
    sqlc.arg(key_hash)::text,
    sqlc.arg(key_prefix)::text,
    sqlc.arg(key_suffix)::text,
    sqlc.arg(key_secret_encrypted)::text,
    sqlc.arg(status)::text,
    false,
    'general',
    sqlc.narg(expires_at)::timestamptz,
    sqlc.narg(quota_limits_json)::text,
    sqlc.narg(availability_schedule_json)::text,
    sqlc.narg(availability_schedule_next_check_at)::timestamptz,
    sqlc.arg(created_at)::timestamptz,
    sqlc.arg(updated_at)::timestamptz
  FROM route_target
  WHERE route_target.route_strategy_status = 'active'
  RETURNING
    id AS api_key_id,
    system_account_id,
    name AS api_key_name,
    description,
    key_prefix,
    key_suffix,
    status AS api_key_status,
    is_default,
    purpose,
    route_strategy_id,
    expires_at,
    quota_limits_json,
    availability_schedule_json
),
hourly_window_upsert AS (
  INSERT INTO juhe_business.request_quota_hourly_window_configs (
    window_hours,
    created_at,
    updated_at
  )
  SELECT
    sqlc.narg(hourly_hours)::integer,
    sqlc.arg(created_at)::timestamptz,
    sqlc.arg(updated_at)::timestamptz
  FROM inserted_api_key
  WHERE sqlc.narg(hourly_hours)::integer IS NOT NULL
  ON CONFLICT (window_hours) DO UPDATE
  SET updated_at = EXCLUDED.updated_at
  RETURNING window_hours
)
SELECT
  inserted_api_key.api_key_id,
  inserted_api_key.system_account_id,
  route_target.system_account_name,
  inserted_api_key.api_key_name,
  inserted_api_key.description,
  inserted_api_key.key_prefix,
  inserted_api_key.key_suffix,
  inserted_api_key.api_key_status,
  inserted_api_key.is_default,
  inserted_api_key.purpose,
  inserted_api_key.route_strategy_id,
  route_target.route_strategy_name,
  route_target.route_strategy_mode,
  route_target.route_strategy_status,
  inserted_api_key.expires_at,
  inserted_api_key.quota_limits_json,
  inserted_api_key.availability_schedule_json,
  hourly_upsert.hourly_upsert_count
FROM route_target
LEFT JOIN inserted_api_key ON true
CROSS JOIN LATERAL (
  SELECT COUNT(*)::int AS hourly_upsert_count
  FROM hourly_window_upsert
) AS hourly_upsert;
