-- name: LockManagementGroupDeleteTarget :one
SELECT
  groups.id,
  groups.system_account_id,
  groups.name,
  groups.provider_code,
  groups.description,
  groups.enabled,
  groups.is_default,
  groups.group_type,
  groups.scheduling_policy_json
FROM juhe_business.groups AS groups
WHERE groups.id = sqlc.arg(group_id)::text
  AND (
    (
      sqlc.arg(can_access_all)::boolean
      AND sqlc.arg(effective_system_account_id)::text = ''
    )
    OR groups.system_account_id = sqlc.arg(effective_system_account_id)::text
  )
FOR UPDATE OF groups;

-- name: LockManagementGroupDeleteRouteStrategies :many
SELECT
  route_strategies.id,
  route_strategies.name
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

-- name: CountManagementGroupDeleteRouteStrategyLoss :one
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

-- name: HardDeleteManagementGroup :one
DELETE FROM juhe_business.groups
WHERE id = sqlc.arg(group_id)::text
  AND system_account_id = sqlc.arg(owner_system_account_id)::text
RETURNING id;

-- name: MarkManagementGroupDeletedStatsDirty :exec
INSERT INTO juhe_business.group_account_stats_dirty (
  group_id,
  reason,
  updated_at
) VALUES (
  sqlc.arg(group_id)::text,
  'group_deleted',
  sqlc.arg(deleted_at)::timestamptz
)
ON CONFLICT (group_id) DO UPDATE SET
  reason = excluded.reason,
  updated_at = excluded.updated_at;
