-- name: FindPublicRouteStrategyTargetByUsername :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE lower(username) = lower($1)
LIMIT 1;

-- name: FindPublicRouteStrategyTargetByID :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1;

-- name: ListPublicRouteStrategies :many
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  (
    SELECT count(*)::bigint
    FROM juhe_business.api_keys
    WHERE api_keys.route_strategy_id = route_strategies.id
      AND api_keys.system_account_id = route_strategies.system_account_id
  ) AS api_key_count,
  route_strategies.created_at,
  route_strategies.updated_at
FROM juhe_business.route_strategies AS route_strategies
WHERE route_strategies.system_account_id = sqlc.arg(system_account_id)
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text AND route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text)
  )
  AND (sqlc.arg(mode)::text = '' OR route_strategies.mode = sqlc.arg(mode))
  AND (sqlc.arg(status)::text = '' OR route_strategies.status = sqlc.arg(status))
ORDER BY route_strategies.updated_at DESC, route_strategies.created_at DESC, route_strategies.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: FindPublicRouteStrategyByID :one
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  (
    SELECT count(*)::bigint
    FROM juhe_business.api_keys
    WHERE api_keys.route_strategy_id = route_strategies.id
      AND api_keys.system_account_id = route_strategies.system_account_id
  ) AS api_key_count,
  route_strategies.created_at,
  route_strategies.updated_at
FROM juhe_business.route_strategies AS route_strategies
WHERE route_strategies.id = $1
LIMIT 1;

-- name: FindPublicRouteStrategyByIDForUpdate :one
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  (
    SELECT count(*)::bigint
    FROM juhe_business.api_keys
    WHERE api_keys.route_strategy_id = route_strategies.id
      AND api_keys.system_account_id = route_strategies.system_account_id
  ) AS api_key_count,
  route_strategies.created_at,
  route_strategies.updated_at
FROM juhe_business.route_strategies AS route_strategies
WHERE route_strategies.id = $1
LIMIT 1
FOR UPDATE;

-- name: ListPublicRouteStrategyBindingsByStrategyIDs :many
WITH requested_bindings AS MATERIALIZED (
  SELECT
    route_strategy_groups.id,
    route_strategy_groups.route_strategy_id,
    route_strategy_groups.group_id,
    route_strategy_groups.system_account_id,
    route_strategy_groups.priority,
    route_strategy_groups.weight,
    route_strategy_groups.status,
    route_strategy_groups.created_at
  FROM juhe_business.route_strategy_groups AS route_strategy_groups
  WHERE route_strategy_groups.route_strategy_id = ANY(sqlc.arg(route_strategy_ids)::text[])
),
binding_group_summaries AS MATERIALIZED (
  SELECT
    route_strategy_groups.id AS binding_id,
    groups.name AS group_name,
    groups.provider_code,
    CASE
      WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled
      WHEN group_authorization.id IS NOT NULL THEN
        CASE
          WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)
          ELSE false
        END
      ELSE false
    END AS group_enabled
  FROM requested_bindings AS route_strategy_groups
  LEFT JOIN juhe_business.groups AS groups
    ON groups.id = route_strategy_groups.group_id
  LEFT JOIN juhe_business.resource_authorizations AS group_authorization
    ON group_authorization.resource_type = 'group'
    AND group_authorization.resource_id = groups.id
    AND group_authorization.resource_owner_system_account_id = groups.system_account_id
    AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
    AND group_authorization.status = 'active'
    AND (
      group_authorization.expires_at IS NULL
      OR group_authorization.expires_at > CURRENT_TIMESTAMP
    )
  LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
    ON group_authorization_settings.authorization_id = group_authorization.id
    AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
    AND group_authorization_settings.group_id = groups.id
)
SELECT
  route_strategy_groups.id,
  route_strategy_groups.route_strategy_id,
  route_strategy_groups.group_id,
  route_strategy_groups.priority,
  route_strategy_groups.weight,
  route_strategy_groups.status,
  binding_group_summaries.group_name,
  binding_group_summaries.provider_code,
  binding_group_summaries.group_enabled
FROM requested_bindings AS route_strategy_groups
LEFT JOIN binding_group_summaries
  ON binding_group_summaries.binding_id = route_strategy_groups.id
ORDER BY route_strategy_groups.route_strategy_id ASC,
  CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
  route_strategy_groups.priority ASC,
  route_strategy_groups.created_at ASC,
  route_strategy_groups.id ASC;

-- name: FindPublicRouteStrategyBindableGroups :many
SELECT
  groups.id,
  groups.system_account_id,
  groups.name,
  groups.provider_code,
  CASE
    WHEN groups.system_account_id = sqlc.arg(system_account_id) THEN groups.enabled
    WHEN group_authorization.id IS NOT NULL THEN
      CASE
        WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)
        ELSE false
      END
    ELSE false
  END AS enabled
FROM juhe_business.groups AS groups
LEFT JOIN juhe_business.resource_authorizations AS group_authorization
  ON group_authorization.resource_type = 'group'
  AND group_authorization.resource_id = groups.id
  AND group_authorization.resource_owner_system_account_id = groups.system_account_id
  AND group_authorization.grantee_system_account_id = sqlc.arg(system_account_id)
  AND group_authorization.status = 'active'
  AND (
    group_authorization.expires_at IS NULL
    OR group_authorization.expires_at > CURRENT_TIMESTAMP
  )
LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
  ON group_authorization_settings.authorization_id = group_authorization.id
  AND group_authorization_settings.system_account_id = sqlc.arg(system_account_id)
  AND group_authorization_settings.group_id = groups.id
WHERE groups.id = ANY(sqlc.arg(group_ids)::text[])
  AND (
    groups.system_account_id = sqlc.arg(system_account_id)
    OR group_authorization.id IS NOT NULL
  )
ORDER BY groups.id ASC
FOR UPDATE OF groups;

-- name: InsertPublicRouteStrategy :one
INSERT INTO juhe_business.route_strategies (
  id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(system_account_id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(mode), sqlc.arg(status),
  false, sqlc.arg(config_json), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING
  id,
  system_account_id,
  name,
  description,
  mode,
  status,
  is_default,
  config_json,
  0::bigint AS api_key_count,
  created_at,
  updated_at;

-- name: UpdatePublicRouteStrategyAllFields :one
UPDATE juhe_business.route_strategies
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    mode = sqlc.arg(mode),
    status = sqlc.arg(status),
    config_json = sqlc.arg(config_json),
    updated_at = sqlc.arg(updated_at)
WHERE route_strategies.id = sqlc.arg(id)
  AND route_strategies.system_account_id = sqlc.arg(system_account_id)
RETURNING
  route_strategies.id,
  route_strategies.system_account_id,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  (
    SELECT count(*)::bigint
    FROM juhe_business.api_keys
    WHERE api_keys.route_strategy_id = route_strategies.id
      AND api_keys.system_account_id = route_strategies.system_account_id
  ) AS api_key_count,
  route_strategies.created_at,
  route_strategies.updated_at;

-- name: DeletePublicRouteStrategyBindings :exec
DELETE FROM juhe_business.route_strategy_groups
WHERE route_strategy_id = sqlc.arg(route_strategy_id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: InsertPublicRouteStrategyBinding :exec
INSERT INTO juhe_business.route_strategy_groups (
  id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(route_strategy_id), sqlc.arg(system_account_id), sqlc.arg(group_id),
  sqlc.arg(priority), sqlc.arg(weight), sqlc.arg(status), sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: DeletePublicRouteStrategy :execrows
DELETE FROM juhe_business.route_strategies
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: CountPublicRouteStrategyAPIKeys :one
SELECT count(*)::bigint AS total
FROM juhe_business.api_keys
WHERE route_strategy_id = sqlc.arg(route_strategy_id)
  AND system_account_id = sqlc.arg(system_account_id);
