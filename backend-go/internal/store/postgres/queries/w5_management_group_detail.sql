-- name: FindManagementGroupDetail :one
WITH visible_group AS (
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
  WHERE groups.id = sqlc.arg(group_id)::text
    AND (
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
  WHERE groups.id = sqlc.arg(group_id)::text
    AND sqlc.arg(system_account_id)::text <> ''
    AND resource_authorizations.resource_type = 'group'
    AND resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
    AND groups.system_account_id <> sqlc.arg(system_account_id)::text
)
SELECT
  visible_group.id,
  visible_group.system_account_id,
  visible_group.system_account_name,
  visible_group.name,
  visible_group.provider_code,
  visible_group.description,
  visible_group.enabled,
  visible_group.is_default,
  visible_group.group_type,
  visible_group.scheduling_policy_json,
  visible_group.access_type,
  visible_group.group_authorization_id,
  visible_group.authorization_status,
  visible_group.authorization_expires_at,
  visible_group.authorization_limits_json,
  visible_group.effective_updated_at
FROM visible_group;

-- name: ListManagementGroupDetailAccountIDs :many
WITH visible_group AS MATERIALIZED (
  SELECT groups.id, groups.system_account_id
  FROM juhe_business.groups AS groups
  WHERE groups.id = sqlc.arg(group_id)::text
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR groups.system_account_id = sqlc.arg(system_account_id)::text
      OR (
        groups.system_account_id <> sqlc.arg(system_account_id)::text
        AND EXISTS (
          SELECT 1
          FROM juhe_business.resource_authorizations AS group_authorizations
          WHERE group_authorizations.resource_type = 'group'
            AND group_authorizations.resource_id = groups.id
            AND group_authorizations.resource_owner_system_account_id = groups.system_account_id
            AND group_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text
            AND group_authorizations.status IN ('active', 'paused', 'expired')
        )
      )
    )
)
SELECT group_accounts.account_id
FROM visible_group
INNER JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.group_id = visible_group.id
  AND group_accounts.system_account_id = visible_group.system_account_id
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
WHERE group_accounts.enabled = true
  AND accounts.deleted_at IS NULL
  AND (
    (
      group_accounts.account_authorization_id IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
    )
    OR account_authorizations.id IS NOT NULL
  )
ORDER BY group_accounts.created_at ASC, group_accounts.account_id ASC;

-- name: ListManagementGroupDetailAuthorizationSources :many
WITH visible_authorization AS MATERIALIZED (
  SELECT resource_authorizations.id
  FROM juhe_business.resource_authorizations AS resource_authorizations
  INNER JOIN juhe_business.groups AS groups
    ON groups.id = resource_authorizations.resource_id
    AND groups.system_account_id = resource_authorizations.resource_owner_system_account_id
  WHERE groups.id = sqlc.arg(group_id)::text
    AND sqlc.arg(system_account_id)::text <> ''
    AND groups.system_account_id <> sqlc.arg(system_account_id)::text
    AND resource_authorizations.resource_type = 'group'
    AND resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
)
SELECT
  authorization_sources.id,
  authorization_sources.authorization_id,
  authorization_sources.source_type,
  coalesce(authorization_sources.source_team_id, '')::text AS source_team_id,
  coalesce(system_teams.name, '')::text AS source_team_name,
  authorization_sources.status,
  authorization_sources.activated_at,
  authorization_sources.ended_at,
  coalesce(authorization_sources.ended_reason, '')::text AS ended_reason,
  authorization_sources.created_by,
  authorization_sources.created_at,
  coalesce(authorization_sources.revoked_by, '')::text AS revoked_by,
  authorization_sources.revoked_at,
  authorization_sources.updated_at
FROM visible_authorization
INNER JOIN juhe_business.resource_authorization_sources AS authorization_sources
  ON authorization_sources.authorization_id = visible_authorization.id
LEFT JOIN juhe_business.system_teams AS system_teams
  ON system_teams.id = authorization_sources.source_team_id
ORDER BY
  CASE authorization_sources.status
    WHEN 'active' THEN 0
    WHEN 'paused' THEN 1
    WHEN 'expired' THEN 2
    WHEN 'revoked' THEN 3
    WHEN 'returned' THEN 4
    ELSE 5
  END ASC,
  authorization_sources.created_at ASC,
  authorization_sources.id ASC;
