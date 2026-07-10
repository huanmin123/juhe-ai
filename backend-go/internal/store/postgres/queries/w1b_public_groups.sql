-- name: FindPublicGroupTargetByUsername :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE lower(username) = lower($1)
LIMIT 1;

-- name: FindPublicGroupTargetByID :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1;

-- name: InsertPublicGroupSystemAccount :exec
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, description, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(username), sqlc.arg(display_name), sqlc.arg(description), 'user', 'active', sqlc.arg(password_hash),
  true, false, sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: FindPublicGroupProviderByCode :one
SELECT code, enabled
FROM juhe_business.providers
WHERE code = $1
LIMIT 1;

-- name: ListPublicGroups :many
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at
FROM juhe_business.groups
WHERE system_account_id = sqlc.arg(system_account_id)
  AND (sqlc.arg(provider_code)::text = '' OR provider_code = sqlc.arg(provider_code))
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (name COLLATE "C" >= sqlc.arg(keyword)::text AND name COLLATE "C" < sqlc.arg(keyword_upper)::text)
    OR (provider_code COLLATE "C" >= sqlc.arg(keyword)::text AND provider_code COLLATE "C" < sqlc.arg(keyword_upper)::text)
  )
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: FindExistingPublicGroupByName :one
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at
FROM juhe_business.groups
WHERE system_account_id = sqlc.arg(system_account_id)
  AND provider_code = sqlc.arg(provider_code)
  AND lower(name) = lower(sqlc.arg(name))
LIMIT 1;

-- name: FindPublicGroupByID :one
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at
FROM juhe_business.groups
WHERE id = $1
LIMIT 1;

-- name: FindPublicGroupByIDForUpdate :one
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at
FROM juhe_business.groups
WHERE id = $1
LIMIT 1
FOR UPDATE;

-- name: InsertPublicGroup :one
INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(system_account_id), sqlc.arg(name), sqlc.arg(provider_code), sqlc.arg(description),
  sqlc.arg(enabled), false, sqlc.arg(group_type), NULL, sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at;

-- name: UpdatePublicGroupAllFields :one
UPDATE juhe_business.groups
SET name = sqlc.arg(name),
    provider_code = sqlc.arg(provider_code),
    description = sqlc.arg(description),
    enabled = sqlc.arg(enabled),
    group_type = sqlc.arg(group_type),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id)
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  created_at,
  updated_at;

-- name: DeletePublicGroup :execrows
DELETE FROM juhe_business.groups
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: CountPublicGroupAccounts :one
SELECT count(*)::bigint AS total
FROM juhe_business.group_accounts
WHERE group_id = $1;

-- name: LockPublicGroupActiveRouteStrategies :many
SELECT route_strategies.id
FROM juhe_business.route_strategy_groups AS target_bindings
JOIN juhe_business.groups AS target_groups
  ON target_groups.id = target_bindings.group_id
  AND target_groups.system_account_id = target_bindings.system_account_id
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = target_bindings.route_strategy_id
  AND route_strategies.system_account_id = target_bindings.system_account_id
WHERE target_bindings.group_id = $1
  AND target_bindings.status = 'active'
  AND target_groups.enabled = true
  AND route_strategies.status = 'active'
ORDER BY route_strategies.id
FOR UPDATE OF route_strategies;

-- name: CountPublicGroupActiveRouteStrategyLoss :one
SELECT count(DISTINCT route_strategies.id)::bigint AS total
FROM juhe_business.route_strategy_groups AS target_bindings
JOIN juhe_business.groups AS target_groups
  ON target_groups.id = target_bindings.group_id
  AND target_groups.system_account_id = target_bindings.system_account_id
JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = target_bindings.route_strategy_id
  AND route_strategies.system_account_id = target_bindings.system_account_id
WHERE target_bindings.group_id = $1
  AND target_bindings.status = 'active'
  AND target_groups.enabled = true
  AND route_strategies.status = 'active'
  AND NOT EXISTS (
    SELECT 1
    FROM juhe_business.route_strategy_groups AS other_bindings
    JOIN juhe_business.groups AS other_groups
      ON other_groups.id = other_bindings.group_id
      AND other_groups.system_account_id = other_bindings.system_account_id
    WHERE other_bindings.route_strategy_id = target_bindings.route_strategy_id
      AND other_bindings.system_account_id = target_bindings.system_account_id
      AND other_bindings.group_id <> target_bindings.group_id
      AND other_bindings.status = 'active'
      AND other_groups.enabled = true
    LIMIT 1
  );
