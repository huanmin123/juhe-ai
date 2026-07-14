-- name: ListManagementRouteStrategies :many
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  route_strategies.created_at,
  route_strategies.updated_at
FROM juhe_business.route_strategies AS route_strategies
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE (
    sqlc.arg(mode)::text = ''
    OR route_strategies.mode = sqlc.arg(mode)::text
  )
  AND (
    sqlc.arg(status)::text = ''
    OR route_strategies.status = sqlc.arg(status)::text
  )
ORDER BY
  route_strategies.updated_at DESC,
  route_strategies.created_at DESC,
  route_strategies.id DESC
LIMIT sqlc.arg(row_limit)::bigint
OFFSET sqlc.arg(row_offset)::bigint;

-- name: ListManagementOwnedRouteStrategies :many
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  route_strategies.created_at,
  route_strategies.updated_at
FROM juhe_business.route_strategies AS route_strategies
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE route_strategies.system_account_id = sqlc.arg(system_account_id)::text
  AND (
    sqlc.arg(mode)::text = ''
    OR route_strategies.mode = sqlc.arg(mode)::text
  )
  AND (
    sqlc.arg(status)::text = ''
    OR route_strategies.status = sqlc.arg(status)::text
  )
ORDER BY
  route_strategies.updated_at DESC,
  route_strategies.created_at DESC,
  route_strategies.id DESC
LIMIT sqlc.arg(row_limit)::bigint
OFFSET sqlc.arg(row_offset)::bigint;

-- name: ListManagementRouteStrategiesByKeyword :many
WITH matched_route_strategy_scopes AS MATERIALIZED (
  SELECT
    route_strategies.id AS route_strategy_id,
    route_strategies.system_account_id
  FROM juhe_business.route_strategies AS route_strategies
  WHERE route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text
    AND route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text
    AND starts_with(route_strategies.name, sqlc.arg(keyword)::text)
)
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  route_strategies.created_at,
  route_strategies.updated_at
FROM matched_route_strategy_scopes
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = matched_route_strategy_scopes.route_strategy_id
  AND route_strategies.system_account_id = matched_route_strategy_scopes.system_account_id
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE (
    sqlc.arg(mode)::text = ''
    OR route_strategies.mode = sqlc.arg(mode)::text
  )
  AND (
    sqlc.arg(status)::text = ''
    OR route_strategies.status = sqlc.arg(status)::text
  )
ORDER BY
  route_strategies.updated_at DESC,
  route_strategies.created_at DESC,
  route_strategies.id DESC
LIMIT sqlc.arg(row_limit)::bigint
OFFSET sqlc.arg(row_offset)::bigint;

-- name: ListManagementOwnedRouteStrategiesByKeyword :many
WITH matched_route_strategy_scopes AS MATERIALIZED (
  SELECT
    route_strategies.id AS route_strategy_id,
    route_strategies.system_account_id
  FROM juhe_business.route_strategies AS route_strategies
  WHERE route_strategies.system_account_id = sqlc.arg(system_account_id)::text
    AND route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text
    AND route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text
    AND starts_with(route_strategies.name, sqlc.arg(keyword)::text)
)
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
  route_strategies.name,
  route_strategies.description,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default,
  route_strategies.config_json,
  route_strategies.created_at,
  route_strategies.updated_at
FROM matched_route_strategy_scopes
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = matched_route_strategy_scopes.route_strategy_id
  AND route_strategies.system_account_id = matched_route_strategy_scopes.system_account_id
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE (
    sqlc.arg(mode)::text = ''
    OR route_strategies.mode = sqlc.arg(mode)::text
  )
  AND (
    sqlc.arg(status)::text = ''
    OR route_strategies.status = sqlc.arg(status)::text
  )
ORDER BY
  route_strategies.updated_at DESC,
  route_strategies.created_at DESC,
  route_strategies.id DESC
LIMIT sqlc.arg(row_limit)::bigint
OFFSET sqlc.arg(row_offset)::bigint;

-- name: ListManagementRouteStrategyListEnrichment :many
WITH requested_scopes AS MATERIALIZED (
  SELECT
    requested.route_strategy_id::text AS route_strategy_id,
    ((sqlc.arg(system_account_ids)::text[])[requested.ordinality::int])::text AS system_account_id,
    requested.ordinality
  FROM unnest(sqlc.arg(route_strategy_ids)::text[])
    WITH ORDINALITY AS requested(route_strategy_id, ordinality)
),
binding_counts AS MATERIALIZED (
  SELECT
    requested_scopes.route_strategy_id,
    requested_scopes.system_account_id,
    count(route_strategy_groups.id)::bigint AS binding_count
  FROM requested_scopes
  LEFT JOIN juhe_business.route_strategy_groups AS route_strategy_groups
    ON route_strategy_groups.route_strategy_id = requested_scopes.route_strategy_id
    AND route_strategy_groups.system_account_id = requested_scopes.system_account_id
  GROUP BY requested_scopes.route_strategy_id, requested_scopes.system_account_id
),
api_key_counts AS MATERIALIZED (
  SELECT
    requested_scopes.route_strategy_id,
    requested_scopes.system_account_id,
    count(api_keys.id)::bigint AS api_key_count
  FROM requested_scopes
  LEFT JOIN juhe_business.api_keys AS api_keys
    ON api_keys.route_strategy_id = requested_scopes.route_strategy_id
    AND api_keys.system_account_id = requested_scopes.system_account_id
  GROUP BY requested_scopes.route_strategy_id, requested_scopes.system_account_id
),
ranked_bindings AS MATERIALIZED (
  SELECT
    requested_scopes.route_strategy_id,
    requested_scopes.system_account_id,
    route_strategy_groups.id AS binding_id,
    route_strategy_groups.group_id,
    groups.name AS group_name,
    groups.provider_code,
    route_strategy_groups.priority,
    route_strategy_groups.weight,
    route_strategy_groups.status AS binding_status,
    CASE
      WHEN groups.id IS NULL THEN false
      WHEN groups.system_account_id = requested_scopes.system_account_id THEN groups.enabled
      WHEN group_authorization.id IS NOT NULL THEN
        CASE
          WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)
          ELSE false
        END
      ELSE false
    END AS group_enabled,
    ROW_NUMBER() OVER (
      PARTITION BY requested_scopes.route_strategy_id, requested_scopes.system_account_id
      ORDER BY
        CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
        route_strategy_groups.priority ASC,
        route_strategy_groups.created_at ASC,
        route_strategy_groups.id ASC
    ) AS row_number
  FROM requested_scopes
  INNER JOIN juhe_business.route_strategy_groups AS route_strategy_groups
    ON route_strategy_groups.route_strategy_id = requested_scopes.route_strategy_id
    AND route_strategy_groups.system_account_id = requested_scopes.system_account_id
  LEFT JOIN juhe_business.groups AS groups
    ON groups.id = route_strategy_groups.group_id
  LEFT JOIN juhe_business.resource_authorizations AS group_authorization
    ON group_authorization.resource_type = 'group'
    AND group_authorization.resource_id = groups.id
    AND group_authorization.resource_owner_system_account_id = groups.system_account_id
    AND group_authorization.grantee_system_account_id = requested_scopes.system_account_id
    AND group_authorization.status = 'active'
    AND (
      group_authorization.expires_at IS NULL
      OR group_authorization.expires_at > CURRENT_TIMESTAMP
    )
  LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
    ON group_authorization_settings.authorization_id = group_authorization.id
    AND group_authorization_settings.system_account_id = requested_scopes.system_account_id
    AND group_authorization_settings.group_id = groups.id
)
SELECT
  requested_scopes.route_strategy_id,
  requested_scopes.system_account_id,
  binding_counts.binding_count,
  api_key_counts.api_key_count,
  ranked_bindings.binding_id,
  ranked_bindings.group_id,
  ranked_bindings.group_name,
  ranked_bindings.provider_code,
  ranked_bindings.priority,
  ranked_bindings.weight,
  ranked_bindings.binding_status,
  ranked_bindings.group_enabled
FROM requested_scopes
INNER JOIN binding_counts
  ON binding_counts.route_strategy_id = requested_scopes.route_strategy_id
  AND binding_counts.system_account_id = requested_scopes.system_account_id
INNER JOIN api_key_counts
  ON api_key_counts.route_strategy_id = requested_scopes.route_strategy_id
  AND api_key_counts.system_account_id = requested_scopes.system_account_id
LEFT JOIN ranked_bindings
  ON ranked_bindings.route_strategy_id = requested_scopes.route_strategy_id
  AND ranked_bindings.system_account_id = requested_scopes.system_account_id
  AND ranked_bindings.row_number <= 3
ORDER BY
  requested_scopes.ordinality ASC,
  ranked_bindings.row_number ASC;

-- name: FindManagementRouteStrategyDetail :one
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
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
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE route_strategies.id = sqlc.arg(route_strategy_id)::text
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR route_strategies.system_account_id = sqlc.arg(system_account_id)::text
  );

-- name: ListManagementRouteStrategyDetailBindings :many
WITH visible_route AS MATERIALIZED (
  SELECT
    route_strategies.id,
    route_strategies.system_account_id
  FROM juhe_business.route_strategies AS route_strategies
  WHERE route_strategies.id = sqlc.arg(route_strategy_id)::text
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR route_strategies.system_account_id = sqlc.arg(system_account_id)::text
    )
)
SELECT
  route_strategy_groups.id,
  route_strategy_groups.group_id,
  groups.name AS group_name,
  groups.provider_code,
  route_strategy_groups.priority,
  route_strategy_groups.weight,
  route_strategy_groups.status,
  CASE
    WHEN groups.id IS NULL THEN false
    WHEN groups.system_account_id = visible_route.system_account_id THEN groups.enabled
    WHEN group_authorization.id IS NOT NULL THEN
      CASE
        WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)
        ELSE false
      END
    ELSE false
  END AS group_enabled
FROM visible_route
INNER JOIN juhe_business.route_strategy_groups AS route_strategy_groups
  ON route_strategy_groups.route_strategy_id = visible_route.id
  AND route_strategy_groups.system_account_id = visible_route.system_account_id
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = route_strategy_groups.group_id
LEFT JOIN juhe_business.resource_authorizations AS group_authorization
  ON group_authorization.resource_type = 'group'
  AND group_authorization.resource_id = groups.id
  AND group_authorization.resource_owner_system_account_id = groups.system_account_id
  AND group_authorization.grantee_system_account_id = visible_route.system_account_id
  AND group_authorization.status = 'active'
  AND (
    group_authorization.expires_at IS NULL
    OR group_authorization.expires_at > CURRENT_TIMESTAMP
  )
LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
  ON group_authorization_settings.authorization_id = group_authorization.id
  AND group_authorization_settings.system_account_id = visible_route.system_account_id
  AND group_authorization_settings.group_id = groups.id
ORDER BY
  CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
  route_strategy_groups.priority ASC,
  route_strategy_groups.created_at ASC,
  route_strategy_groups.id ASC
LIMIT 21;
