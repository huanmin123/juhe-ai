-- name: CreateManagementSystemAccount :one
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, description, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(username)::text,
  sqlc.arg(display_name)::text,
  sqlc.narg(description)::text,
  sqlc.arg(role)::text,
  sqlc.arg(status)::text,
  sqlc.arg(password_hash)::text,
  sqlc.arg(must_change_password)::boolean,
  sqlc.arg(image_generation_enabled)::boolean,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING
  id,
  username,
  display_name,
  description,
  role,
  status,
  must_change_password,
  image_generation_enabled,
  last_login_at,
  created_at,
  updated_at;

-- name: CreateManagementDefaultGroup :one
INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, description,
  enabled, is_default, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(name)::text,
  sqlc.arg(provider_code)::text,
  sqlc.narg(description)::text,
  true,
  true,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING id;

-- name: CreateManagementDefaultRouteStrategy :one
INSERT INTO juhe_business.route_strategies (
  id, system_account_id, name, description, mode, status, is_default,
  config_json, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(name)::text,
  sqlc.narg(description)::text,
  'normal',
  'active',
  true,
  NULL,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING id;

-- name: CreateManagementDefaultRouteStrategyGroup :exec
INSERT INTO juhe_business.route_strategy_groups (
  id, route_strategy_id, system_account_id, group_id, priority, weight,
  status, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(route_strategy_id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(group_id)::text,
  1,
  1,
  'active',
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
);

-- name: CreateManagementDefaultAPIKey :one
INSERT INTO juhe_business.api_keys (
  id, system_account_id, route_strategy_id, name, description, key_hash,
  key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose,
  expires_at, quota_limits_json, availability_schedule_json,
  availability_schedule_next_check_at, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(route_strategy_id)::text,
  sqlc.arg(name)::text,
  sqlc.narg(description)::text,
  sqlc.arg(key_hash)::text,
  sqlc.arg(key_prefix)::text,
  sqlc.arg(key_suffix)::text,
  sqlc.narg(key_secret_encrypted)::text,
  'active',
  sqlc.arg(is_default)::boolean,
  sqlc.arg(purpose)::text,
  NULL,
  NULL,
  NULL,
  NULL,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
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
  last_used_at,
  created_at,
  updated_at;

-- name: CountManagementDefaultGroupsForProvider :one
SELECT COUNT(*)::int
FROM juhe_business.groups
WHERE system_account_id = sqlc.arg(system_account_id)::text
  AND provider_code = sqlc.arg(provider_code)::text
  AND is_default = true;
