-- name: FindManagementAPIKeySecret :one
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.name,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.key_secret_encrypted
FROM juhe_business.api_keys AS api_keys
WHERE api_keys.id = sqlc.arg(api_key_id)::text
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR api_keys.system_account_id = sqlc.arg(system_account_id)::text
  );

-- name: LockManagementAPIKeySecretRefreshTarget :one
SELECT
  api_keys.id,
  api_keys.system_account_id,
  system_accounts.display_name AS system_account_name,
  api_keys.name,
  api_keys.description,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.status,
  api_keys.is_default,
  api_keys.purpose,
  api_keys.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  api_keys.availability_schedule_json
FROM juhe_business.api_keys AS api_keys
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = api_keys.system_account_id
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE api_keys.id = sqlc.arg(api_key_id)::text
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR api_keys.system_account_id = sqlc.arg(system_account_id)::text
  )
FOR UPDATE OF api_keys;

-- name: UpdateManagementAPIKeySecret :one
UPDATE juhe_business.api_keys
SET key_hash = sqlc.arg(key_hash)::text,
    key_prefix = sqlc.arg(key_prefix)::text,
    key_suffix = sqlc.arg(key_suffix)::text,
    key_secret_encrypted = sqlc.arg(key_secret_encrypted)::text,
    updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(api_key_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text
RETURNING id;
