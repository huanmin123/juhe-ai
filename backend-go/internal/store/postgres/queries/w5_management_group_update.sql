-- name: FindManagementGroupUpdateProvider :one
SELECT code, enabled
FROM juhe_business.providers
WHERE code = sqlc.arg(provider_code)::text
FOR SHARE;

-- name: LockManagementGroupUpdateTarget :one
SELECT
  groups.id,
  groups.system_account_id,
  groups.name,
  groups.provider_code,
  groups.description,
  groups.enabled,
  groups.is_default,
  groups.group_type,
  groups.scheduling_policy_json,
  CASE
    WHEN (
      sqlc.arg(can_access_all)::boolean
      AND sqlc.arg(effective_system_account_id)::text = ''
    ) OR groups.system_account_id = sqlc.arg(effective_system_account_id)::text
      THEN 'owner'::text
    ELSE 'authorized'::text
  END AS access_type,
  coalesce(visible_authorization.id, '')::text AS group_authorization_id
FROM juhe_business.groups AS groups
LEFT JOIN LATERAL (
  SELECT resource_authorizations.id
  FROM juhe_business.resource_authorizations AS resource_authorizations
  WHERE sqlc.arg(effective_system_account_id)::text <> ''
    AND resource_authorizations.resource_type = 'group'
    AND resource_authorizations.resource_id = groups.id
    AND resource_authorizations.resource_owner_system_account_id = groups.system_account_id
    AND resource_authorizations.grantee_system_account_id = sqlc.arg(effective_system_account_id)::text
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
  ORDER BY resource_authorizations.id ASC
  LIMIT 1
) AS visible_authorization ON true
WHERE groups.id = sqlc.arg(group_id)::text
  AND (
    (
      sqlc.arg(can_access_all)::boolean
      AND sqlc.arg(effective_system_account_id)::text = ''
    )
    OR groups.system_account_id = sqlc.arg(effective_system_account_id)::text
    OR visible_authorization.id IS NOT NULL
  )
FOR UPDATE OF groups;

-- name: LockManagementGroupUpdateAuthorization :one
SELECT
  resource_authorizations.id,
  resource_authorizations.resource_owner_system_account_id,
  resource_authorizations.grantee_system_account_id,
  resource_authorizations.status,
  resource_authorizations.expires_at
FROM juhe_business.resource_authorizations AS resource_authorizations
WHERE resource_authorizations.id = sqlc.arg(authorization_id)::text
  AND resource_authorizations.resource_type = 'group'
  AND resource_authorizations.resource_id = sqlc.arg(group_id)::text
  AND resource_authorizations.resource_owner_system_account_id = sqlc.arg(owner_system_account_id)::text
  AND resource_authorizations.grantee_system_account_id = sqlc.arg(effective_system_account_id)::text
  AND resource_authorizations.status IN ('active', 'paused', 'expired')
FOR UPDATE OF resource_authorizations;

-- name: LockManagementGroupUpdateAuthorizationSettings :one
SELECT
  system_account_id,
  group_id,
  enabled,
  group_type,
  scheduling_policy_json,
  created_at,
  updated_at
FROM juhe_business.group_authorization_settings
WHERE authorization_id = sqlc.arg(authorization_id)::text
FOR UPDATE;

-- name: CountManagementGroupUpdateAccounts :one
SELECT count(*)::bigint AS total
FROM juhe_business.group_accounts AS group_accounts
INNER JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
INNER JOIN juhe_business.accounts AS accounts
  ON accounts.id = group_accounts.account_id
  AND accounts.system_account_id = group_accounts.system_account_id
LEFT JOIN juhe_business.resource_authorizations AS account_authorizations
  ON account_authorizations.id = group_accounts.account_authorization_id
  AND account_authorizations.id = accounts.authorization_instance_authorization_id
  AND account_authorizations.resource_type = 'account'
  AND account_authorizations.resource_id = accounts.authorization_instance_source_account_id
  AND account_authorizations.resource_owner_system_account_id = accounts.authorization_instance_owner_system_account_id
  AND account_authorizations.grantee_system_account_id = accounts.system_account_id
  AND account_authorizations.status IN ('active', 'paused', 'expired')
WHERE group_accounts.group_id = sqlc.arg(group_id)::text
  AND group_accounts.system_account_id = sqlc.arg(owner_system_account_id)::text
  AND group_accounts.enabled = true
  AND accounts.deleted_at IS NULL
  AND (
    (
      group_accounts.account_authorization_id IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
    )
    OR account_authorizations.id IS NOT NULL
  );

-- name: LockManagementGroupUpdateRouteStrategies :many
SELECT route_strategies.id
FROM juhe_business.route_strategy_groups AS target_bindings
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = target_bindings.route_strategy_id
  AND route_strategies.system_account_id = target_bindings.system_account_id
INNER JOIN juhe_business.groups AS target_groups
  ON target_groups.id = target_bindings.group_id
LEFT JOIN juhe_business.resource_authorizations AS target_authorization
  ON target_authorization.resource_type = 'group'
  AND target_authorization.resource_id = target_groups.id
  AND target_authorization.resource_owner_system_account_id = target_groups.system_account_id
  AND target_authorization.grantee_system_account_id = target_bindings.system_account_id
  AND target_authorization.status = 'active'
  AND (
    target_authorization.expires_at IS NULL
    OR target_authorization.expires_at > sqlc.arg(now_at)::timestamptz
  )
LEFT JOIN juhe_business.group_authorization_settings AS target_settings
  ON target_settings.authorization_id = target_authorization.id
  AND target_settings.system_account_id = target_bindings.system_account_id
  AND target_settings.group_id = target_groups.id
WHERE target_bindings.group_id = sqlc.arg(group_id)::text
  AND (
    sqlc.arg(all_scopes)::boolean
    OR target_bindings.system_account_id = sqlc.arg(effective_system_account_id)::text
  )
  AND target_bindings.status = 'active'
  AND route_strategies.status = 'active'
  AND target_groups.enabled = true
  AND (
    target_groups.system_account_id = target_bindings.system_account_id
    OR (
      target_authorization.id IS NOT NULL
      AND coalesce(target_settings.enabled, true) = true
    )
  )
ORDER BY route_strategies.id ASC
LIMIT 101
FOR UPDATE OF route_strategies;

-- name: CountManagementGroupUpdateRouteStrategyLoss :one
SELECT count(DISTINCT route_strategies.id)::bigint AS total
FROM juhe_business.route_strategy_groups AS target_bindings
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = target_bindings.route_strategy_id
  AND route_strategies.system_account_id = target_bindings.system_account_id
INNER JOIN juhe_business.groups AS target_groups
  ON target_groups.id = target_bindings.group_id
LEFT JOIN juhe_business.resource_authorizations AS target_authorization
  ON target_authorization.resource_type = 'group'
  AND target_authorization.resource_id = target_groups.id
  AND target_authorization.resource_owner_system_account_id = target_groups.system_account_id
  AND target_authorization.grantee_system_account_id = target_bindings.system_account_id
  AND target_authorization.status = 'active'
  AND (
    target_authorization.expires_at IS NULL
    OR target_authorization.expires_at > sqlc.arg(now_at)::timestamptz
  )
LEFT JOIN juhe_business.group_authorization_settings AS target_settings
  ON target_settings.authorization_id = target_authorization.id
  AND target_settings.system_account_id = target_bindings.system_account_id
  AND target_settings.group_id = target_groups.id
WHERE target_bindings.group_id = sqlc.arg(group_id)::text
  AND (
    sqlc.arg(all_scopes)::boolean
    OR target_bindings.system_account_id = sqlc.arg(effective_system_account_id)::text
  )
  AND route_strategies.id = ANY(sqlc.arg(route_strategy_ids)::text[])
  AND target_bindings.status = 'active'
  AND route_strategies.status = 'active'
  AND target_groups.enabled = true
  AND (
    target_groups.system_account_id = target_bindings.system_account_id
    OR (
      target_authorization.id IS NOT NULL
      AND coalesce(target_settings.enabled, true) = true
    )
  )
  AND NOT EXISTS (
    SELECT 1
    FROM juhe_business.route_strategy_groups AS other_bindings
    INNER JOIN juhe_business.groups AS other_groups
      ON other_groups.id = other_bindings.group_id
    LEFT JOIN juhe_business.resource_authorizations AS other_authorization
      ON other_authorization.resource_type = 'group'
      AND other_authorization.resource_id = other_groups.id
      AND other_authorization.resource_owner_system_account_id = other_groups.system_account_id
      AND other_authorization.grantee_system_account_id = other_bindings.system_account_id
      AND other_authorization.status = 'active'
      AND (
        other_authorization.expires_at IS NULL
        OR other_authorization.expires_at > sqlc.arg(now_at)::timestamptz
      )
    LEFT JOIN juhe_business.group_authorization_settings AS other_settings
      ON other_settings.authorization_id = other_authorization.id
      AND other_settings.system_account_id = other_bindings.system_account_id
      AND other_settings.group_id = other_groups.id
    WHERE other_bindings.route_strategy_id = target_bindings.route_strategy_id
      AND other_bindings.system_account_id = target_bindings.system_account_id
      AND other_bindings.group_id <> target_bindings.group_id
      AND other_bindings.status = 'active'
      AND other_groups.enabled = true
      AND (
        other_groups.system_account_id = other_bindings.system_account_id
        OR (
          other_authorization.id IS NOT NULL
          AND coalesce(other_settings.enabled, true) = true
        )
      )
  );

-- name: UpdateManagementGroupOwner :one
UPDATE juhe_business.groups
SET
  name = sqlc.arg(name)::text,
  provider_code = sqlc.arg(provider_code)::text,
  description = sqlc.narg(description)::text,
  enabled = sqlc.arg(enabled)::boolean,
  group_type = sqlc.arg(group_type)::text,
  scheduling_policy_json = sqlc.narg(scheduling_policy_json)::text,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(group_id)::text
  AND system_account_id = sqlc.arg(owner_system_account_id)::text
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  description,
  enabled,
  is_default,
  group_type,
  scheduling_policy_json;

-- name: UpsertManagementGroupAuthorizationSettings :one
INSERT INTO juhe_business.group_authorization_settings (
  authorization_id,
  system_account_id,
  group_id,
  enabled,
  group_type,
  scheduling_policy_json,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(authorization_id)::text,
  sqlc.arg(effective_system_account_id)::text,
  sqlc.arg(group_id)::text,
  sqlc.arg(enabled)::boolean,
  sqlc.arg(group_type)::text,
  sqlc.narg(scheduling_policy_json)::text,
  sqlc.arg(updated_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
ON CONFLICT (authorization_id) DO UPDATE SET
  system_account_id = EXCLUDED.system_account_id,
  group_id = EXCLUDED.group_id,
  enabled = EXCLUDED.enabled,
  group_type = EXCLUDED.group_type,
  scheduling_policy_json = EXCLUDED.scheduling_policy_json,
  updated_at = EXCLUDED.updated_at
RETURNING
  authorization_id,
  system_account_id,
  group_id,
  enabled,
  group_type,
  scheduling_policy_json;
