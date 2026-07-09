-- name: ListManagementGroupAccountOptions :many
WITH group_rows AS (
  SELECT
    groups.id,
    groups.system_account_id,
    system_accounts.display_name AS system_account_name,
    groups.name,
    groups.provider_code,
    groups.enabled,
    groups.is_default,
    groups.group_type,
    groups.scheduling_policy_json,
    groups.updated_at,
    'owner'::text AS access_type,
    NULL::text AS group_authorization_id,
    NULL::text AS authorization_status,
    NULL::timestamptz AS authorization_expires_at,
    NULL::text AS authorization_limits_json,
    false AS has_active_manual_authorization_source
  FROM juhe_business.groups AS groups
  LEFT JOIN juhe_business.system_accounts AS system_accounts
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
    coalesce(group_authorization_settings.updated_at, groups.updated_at) AS updated_at,
    'authorized'::text AS access_type,
    resource_authorizations.id AS group_authorization_id,
    resource_authorizations.status AS authorization_status,
    resource_authorizations.expires_at AS authorization_expires_at,
    resource_authorizations.limits_json::text AS authorization_limits_json,
    EXISTS (
      SELECT 1
      FROM juhe_business.resource_authorization_sources AS returnable_sources
      WHERE returnable_sources.authorization_id = resource_authorizations.id
        AND returnable_sources.source_type = 'manual'
        AND returnable_sources.status = 'active'
    ) AS has_active_manual_authorization_source
  FROM juhe_business.resource_authorizations AS resource_authorizations
  INNER JOIN juhe_business.groups AS groups
    ON groups.id = resource_authorizations.resource_id
    AND groups.system_account_id = resource_authorizations.resource_owner_system_account_id
  LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
    ON group_authorization_settings.authorization_id = resource_authorizations.id
    AND group_authorization_settings.system_account_id = resource_authorizations.grantee_system_account_id
    AND group_authorization_settings.group_id = resource_authorizations.resource_id
  LEFT JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = groups.system_account_id
  WHERE sqlc.arg(system_account_id)::text <> ''
    AND sqlc.arg(manageable_only)::boolean = false
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
  group_rows.enabled,
  group_rows.is_default,
  group_rows.group_type,
  group_rows.scheduling_policy_json,
  group_rows.access_type,
  group_rows.group_authorization_id,
  group_rows.authorization_status,
  group_rows.authorization_expires_at,
  group_rows.authorization_limits_json,
  group_rows.has_active_manual_authorization_source
FROM group_rows
WHERE true
  AND (
    coalesce(array_length(sqlc.arg(ids)::text[], 1), 0) = 0
    OR group_rows.id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(provider_code)::text = ''
    OR group_rows.provider_code = sqlc.arg(provider_code)::text
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      (
        group_rows.name COLLATE "C" >= sqlc.arg(keyword)::text
        AND group_rows.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      )
      OR (
        group_rows.provider_code COLLATE "C" >= sqlc.arg(keyword)::text
        AND group_rows.provider_code COLLATE "C" < sqlc.arg(keyword_upper)::text
      )
    )
  )
ORDER BY
  CASE WHEN sqlc.arg(prefer_default)::boolean THEN group_rows.is_default ELSE false END DESC,
  group_rows.updated_at DESC,
  group_rows.id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListManagementGroupAccountOptionIDs :many
SELECT
  group_accounts.group_id,
  group_accounts.account_id
FROM juhe_business.group_accounts AS group_accounts
INNER JOIN juhe_business.accounts AS accounts
  ON accounts.id = group_accounts.account_id
  AND accounts.system_account_id = group_accounts.system_account_id
WHERE group_accounts.group_id = ANY(sqlc.arg(group_ids)::text[])
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR group_accounts.system_account_id = sqlc.arg(system_account_id)::text
  )
  AND group_accounts.enabled = true
  AND group_accounts.account_authorization_id IS NULL
  AND accounts.deleted_at IS NULL
ORDER BY group_accounts.group_id ASC, group_accounts.created_at ASC, group_accounts.account_id ASC;
