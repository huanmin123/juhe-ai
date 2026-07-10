-- name: ListManagementGroups :many
WITH group_rows AS (
  SELECT
    groups.id,
    groups.system_account_id,
    system_accounts.display_name AS system_account_name,
    groups.name,
    groups.provider_code,
    groups.description,
    groups.enabled,
    groups.is_default,
    groups.group_type,
    groups.scheduling_policy_json,
    'owner'::text AS access_type,
    NULL::text AS group_authorization_id,
    NULL::text AS authorization_status,
    NULL::timestamptz AS authorization_expires_at,
    NULL::text AS authorization_limits_json,
    groups.updated_at AS effective_updated_at
  FROM juhe_business.groups AS groups
  INNER JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = groups.system_account_id
  WHERE (
    sqlc.arg(system_account_id)::text = ''
    OR groups.system_account_id = sqlc.arg(system_account_id)::text
  )

  UNION ALL

  SELECT
    groups.id,
    groups.system_account_id,
    system_accounts.display_name AS system_account_name,
    groups.name,
    groups.provider_code,
    groups.description,
    CASE
      WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)
      ELSE false
    END AS enabled,
    false AS is_default,
    coalesce(group_authorization_settings.group_type, groups.group_type) AS group_type,
    CASE
      WHEN coalesce(group_authorization_settings.group_type, groups.group_type) = 'high_concurrency'
        THEN coalesce(group_authorization_settings.scheduling_policy_json, groups.scheduling_policy_json)
      ELSE NULL
    END AS scheduling_policy_json,
    'authorized'::text AS access_type,
    resource_authorizations.id AS group_authorization_id,
    resource_authorizations.status AS authorization_status,
    resource_authorizations.expires_at AS authorization_expires_at,
    resource_authorizations.limits_json AS authorization_limits_json,
    coalesce(group_authorization_settings.updated_at, groups.updated_at) AS effective_updated_at
  FROM juhe_business.resource_authorizations AS resource_authorizations
  INNER JOIN juhe_business.groups AS groups
    ON groups.id = resource_authorizations.resource_id
    AND groups.system_account_id = resource_authorizations.resource_owner_system_account_id
  INNER JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = groups.system_account_id
  LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
    ON group_authorization_settings.authorization_id = resource_authorizations.id
    AND group_authorization_settings.system_account_id = resource_authorizations.grantee_system_account_id
    AND group_authorization_settings.group_id = resource_authorizations.resource_id
  WHERE sqlc.arg(system_account_id)::text <> ''
    AND resource_authorizations.resource_type = 'group'
    AND resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
    AND groups.system_account_id <> sqlc.arg(system_account_id)::text
)
SELECT
  group_rows.id,
  group_rows.system_account_id,
  group_rows.system_account_name,
  group_rows.name,
  group_rows.provider_code,
  group_rows.description,
  group_rows.enabled,
  group_rows.is_default,
  group_rows.group_type,
  group_rows.scheduling_policy_json,
  group_rows.access_type,
  group_rows.group_authorization_id,
  group_rows.authorization_status,
  group_rows.authorization_expires_at,
  group_rows.authorization_limits_json,
  group_rows.effective_updated_at
FROM group_rows
ORDER BY group_rows.effective_updated_at DESC, group_rows.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementGroupAccountStats :many
SELECT
  system_account_id,
  group_id,
  total,
  available,
  active,
  disabled,
  error,
  rate_limited,
  current_concurrency,
  concurrency_limit
FROM juhe_stats.group_account_stats
WHERE group_id = ANY(sqlc.arg(group_ids)::text[])
ORDER BY group_id ASC, system_account_id ASC;

-- name: ListManagementGroupUsageTotals :many
WITH requested_scopes AS MATERIALIZED (
  SELECT
    requested.lookup_key::text AS lookup_key,
    (sqlc.arg(system_account_ids)::text[])[requested.ordinality::int] AS system_account_id,
    (sqlc.arg(scope_types)::text[])[requested.ordinality::int] AS scope_type,
    (sqlc.arg(scope_ids)::text[])[requested.ordinality::int] AS scope_id,
    requested.ordinality
  FROM unnest(sqlc.arg(lookup_keys)::text[])
    WITH ORDINALITY AS requested(lookup_key, ordinality)
)
SELECT
  requested_scopes.lookup_key,
  usage_stats.system_account_id,
  usage_stats.scope_type,
  usage_stats.scope_id,
  usage_stats.request_count,
  usage_stats.input_tokens,
  usage_stats.output_tokens,
  usage_stats.cache_read_tokens,
  usage_stats.cache_read_cost_usd,
  usage_stats.cache_write_tokens,
  usage_stats.cache_write_1h_tokens,
  usage_stats.cache_write_cost_usd,
  usage_stats.thinking_tokens,
  usage_stats.input_image_tokens,
  usage_stats.output_image_tokens,
  usage_stats.total_cost_usd,
  usage_stats.last_used_at
FROM requested_scopes
INNER JOIN juhe_stats.usage_stats_totals AS usage_stats
  ON usage_stats.system_account_id = requested_scopes.system_account_id
  AND usage_stats.scope_type = requested_scopes.scope_type
  AND usage_stats.scope_id = requested_scopes.scope_id
ORDER BY requested_scopes.ordinality ASC;

-- name: ListManagementGroupUsageDaily :many
WITH requested_scopes AS MATERIALIZED (
  SELECT
    requested.lookup_key::text AS lookup_key,
    (sqlc.arg(system_account_ids)::text[])[requested.ordinality::int] AS system_account_id,
    (sqlc.arg(scope_types)::text[])[requested.ordinality::int] AS scope_type,
    (sqlc.arg(scope_ids)::text[])[requested.ordinality::int] AS scope_id,
    requested.ordinality
  FROM unnest(sqlc.arg(lookup_keys)::text[])
    WITH ORDINALITY AS requested(lookup_key, ordinality)
)
SELECT
  requested_scopes.lookup_key,
  usage_stats.system_account_id,
  usage_stats.scope_type,
  usage_stats.scope_id,
  usage_stats.request_count,
  usage_stats.input_tokens,
  usage_stats.output_tokens,
  usage_stats.cache_read_tokens,
  usage_stats.cache_read_cost_usd,
  usage_stats.cache_write_tokens,
  usage_stats.cache_write_1h_tokens,
  usage_stats.cache_write_cost_usd,
  usage_stats.thinking_tokens,
  usage_stats.input_image_tokens,
  usage_stats.output_image_tokens,
  usage_stats.total_cost_usd,
  usage_stats.last_used_at
FROM requested_scopes
INNER JOIN juhe_stats.usage_stats_daily AS usage_stats
  ON usage_stats.system_account_id = requested_scopes.system_account_id
  AND usage_stats.scope_type = requested_scopes.scope_type
  AND usage_stats.scope_id = requested_scopes.scope_id
  AND usage_stats.stat_date = sqlc.arg(stat_date)::text
ORDER BY requested_scopes.ordinality ASC;

-- name: ListManagementGroupAuthorizationSources :many
SELECT
  authorization_sources.authorization_id,
  authorization_sources.source_type,
  authorization_sources.status,
  coalesce(system_teams.name, '')::text AS source_team_name
FROM juhe_business.resource_authorization_sources AS authorization_sources
LEFT JOIN juhe_business.system_teams AS system_teams
  ON system_teams.id = authorization_sources.source_team_id
WHERE authorization_sources.authorization_id = ANY(sqlc.arg(authorization_ids)::text[])
ORDER BY
  authorization_sources.authorization_id ASC,
  authorization_sources.created_at ASC,
  authorization_sources.id ASC;
