-- name: ListManagementAccountTags :many
SELECT
  account_tags.id,
  account_tags.name,
  count(accounts.id)::bigint AS account_count,
  account_tags.created_at,
  account_tags.updated_at
FROM juhe_business.account_tags AS account_tags
LEFT JOIN juhe_business.account_tag_bindings AS account_tag_bindings
  ON account_tag_bindings.tag_id = account_tags.id
  AND account_tag_bindings.system_account_id = account_tags.system_account_id
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = account_tag_bindings.account_id
  AND accounts.system_account_id = account_tag_bindings.system_account_id
  AND accounts.deleted_at IS NULL
WHERE account_tags.system_account_id = sqlc.arg(system_account_id)::text
GROUP BY
  account_tags.id,
  account_tags.name,
  account_tags.created_at,
  account_tags.updated_at
ORDER BY account_tags.name ASC, account_tags.id ASC;

-- name: LockManagementAccountTagForDelete :one
SELECT id
FROM juhe_business.account_tags
WHERE id = sqlc.arg(tag_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text
FOR UPDATE;

-- name: ManagementAccountTagHasActiveBindings :one
SELECT EXISTS (
  SELECT 1
  FROM juhe_business.account_tag_bindings AS account_tag_bindings
  INNER JOIN juhe_business.accounts AS accounts
    ON accounts.id = account_tag_bindings.account_id
    AND accounts.system_account_id = account_tag_bindings.system_account_id
    AND accounts.deleted_at IS NULL
  LEFT JOIN juhe_business.resource_authorizations AS visible_authorizations
    ON visible_authorizations.id = accounts.authorization_instance_authorization_id
    AND visible_authorizations.resource_type = 'account'
    AND visible_authorizations.grantee_system_account_id = accounts.system_account_id
    AND visible_authorizations.status IN ('active', 'paused', 'expired')
  WHERE account_tag_bindings.tag_id = sqlc.arg(tag_id)::text
    AND account_tag_bindings.system_account_id = sqlc.arg(system_account_id)::text
    AND (
      accounts.authorization_instance_authorization_id IS NULL
      OR visible_authorizations.id IS NOT NULL
    )
  LIMIT 1
)::boolean;

-- name: DeleteManagementAccountTag :execrows
DELETE FROM juhe_business.account_tags
WHERE id = sqlc.arg(tag_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text;

-- name: LockManagementAccountForTagUpdate :one
SELECT accounts.id
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.resource_authorizations AS authorizations
  ON authorizations.id = accounts.authorization_instance_authorization_id
  AND authorizations.resource_type = 'account'
  AND authorizations.grantee_system_account_id = accounts.system_account_id
  AND authorizations.resource_id = accounts.authorization_instance_source_account_id
WHERE accounts.id = sqlc.arg(account_id)::text
  AND accounts.system_account_id = sqlc.arg(system_account_id)::text
  AND accounts.deleted_at IS NULL
  AND (
    accounts.authorization_instance_authorization_id IS NULL
    OR authorizations.status IN ('active', 'paused', 'expired')
  )
FOR UPDATE OF accounts;

-- name: DeleteManagementAccountTagBindingsForAccount :exec
DELETE FROM juhe_business.account_tag_bindings
WHERE account_id = sqlc.arg(account_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text;

-- name: UpsertManagementAccountTagForAccount :one
INSERT INTO juhe_business.account_tags (
  id, system_account_id, name, created_at, updated_at
) VALUES (
  sqlc.arg(tag_id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(name)::text,
  now(),
  now()
)
ON CONFLICT (system_account_id, name) DO UPDATE SET
  name = EXCLUDED.name
RETURNING id, name, created_at, updated_at;

-- name: InsertManagementAccountTagBindingForAccount :exec
INSERT INTO juhe_business.account_tag_bindings (
  account_id, tag_id, system_account_id, created_at
) VALUES (
  sqlc.arg(account_id)::text,
  sqlc.arg(tag_id)::text,
  sqlc.arg(system_account_id)::text,
  now()
)
ON CONFLICT (account_id, tag_id) DO NOTHING;

-- name: GetManagementAccountTagUpdateSummary :one
SELECT
  accounts.id,
  accounts.system_account_id,
  system_accounts.display_name AS system_account_name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.name,
  accounts.notes,
  accounts.type,
  (CASE
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND (
        option_group_bindings.group_id IS NULL
        OR option_group_bindings.account_authorization_id IS NULL
        OR option_group_bindings.account_authorization_id <> authorizations.id
      )
    THEN 'disabled'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND (
        authorizations.status <> 'active'
        OR (authorizations.expires_at IS NOT NULL AND authorizations.expires_at <= now())
      )
    THEN 'disabled'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND source_accounts.id IS NULL
    THEN 'disabled'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND (
        source_accounts.last_error_code = 'account_expired'
        OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at <= now())
      )
    THEN 'disabled'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable')
    THEN source_accounts.status
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND source_accounts.cooldown_until IS NOT NULL
      AND source_accounts.cooldown_until > now()
    THEN 'temporary_unavailable'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      AND source_accounts.schedulable = false
    THEN 'disabled'
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= now())
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now() THEN 'temporary_unavailable'
    WHEN accounts.schedulable = false THEN 'disabled'
    ELSE accounts.status
  END)::text AS status,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.super_priority_enabled,
  accounts.fallback_enabled,
  accounts.client_compatibility,
  (CASE
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN (
      option_group_bindings.group_id IS NOT NULL
      AND option_group_bindings.account_authorization_id IS NOT NULL
      AND option_group_bindings.account_authorization_id = authorizations.id
      AND authorizations.status = 'active'
      AND (authorizations.expires_at IS NULL OR authorizations.expires_at > now())
      AND source_accounts.id IS NOT NULL
      AND source_accounts.status = 'active'
      AND source_accounts.schedulable = true
      AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= now())
      AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > now())
      AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
      AND accounts.status = 'active'
      AND accounts.schedulable = true
      AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= now())
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > now())
      AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
    )
    ELSE (
      accounts.status = 'active'
      AND accounts.schedulable = true
      AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= now())
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > now())
      AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
    )
  END)::boolean AS schedulable,
  accounts.availability_schedule_json,
  accounts.account_expires_at,
  accounts.cooldown_until,
  accounts.last_error_code,
  accounts.last_error_message,
  COALESCE(group_bindings.group_id, '') AS bound_group_id,
  COALESCE(bound_groups.name, '') AS bound_group_name,
  CASE
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
    ELSE 'owner'
  END AS access_type,
  accounts.authorization_instance_authorization_id AS account_authorization_id,
  authorizations.status AS authorization_status,
  authorizations.expires_at AS authorization_expires_at,
  accounts.authorization_instance_source_account_id,
  accounts.authorization_instance_owner_system_account_id,
  COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id,
  COALESCE(owner_accounts.display_name, '') AS owner_system_account_name
FROM juhe_business.accounts AS accounts
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = accounts.system_account_id
LEFT JOIN juhe_business.resource_authorizations AS authorizations
  ON authorizations.id = accounts.authorization_instance_authorization_id
  AND authorizations.resource_type = 'account'
  AND authorizations.grantee_system_account_id = accounts.system_account_id
  AND authorizations.resource_id = accounts.authorization_instance_source_account_id
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = accounts.authorization_instance_source_account_id
  AND source_accounts.deleted_at IS NULL
LEFT JOIN juhe_business.system_accounts AS owner_accounts
  ON owner_accounts.id = COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id)
LEFT JOIN LATERAL (
  SELECT group_accounts.group_id
  FROM juhe_business.group_accounts AS group_accounts
  WHERE group_accounts.account_id = accounts.id
    AND group_accounts.system_account_id = accounts.system_account_id
    AND group_accounts.enabled = true
  ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
  LIMIT 1
) AS group_bindings ON true
LEFT JOIN LATERAL (
  SELECT
    group_accounts.group_id,
    group_accounts.account_authorization_id
  FROM juhe_business.group_accounts AS group_accounts
  WHERE group_accounts.account_id = accounts.id
    AND group_accounts.system_account_id = accounts.system_account_id
    AND group_accounts.enabled = true
    AND group_accounts.account_authorization_id = authorizations.id
  ORDER BY group_accounts.created_at ASC, group_accounts.group_id ASC
  LIMIT 1
) AS option_group_bindings ON true
LEFT JOIN juhe_business.groups AS bound_groups
  ON bound_groups.id = group_bindings.group_id
WHERE accounts.id = sqlc.arg(account_id)::text
  AND accounts.system_account_id = sqlc.arg(system_account_id)::text
  AND accounts.deleted_at IS NULL
  AND (
    accounts.authorization_instance_authorization_id IS NULL
    OR authorizations.status IN ('active', 'paused', 'expired')
  )
LIMIT 1;

-- name: ListManagementAccountTagsForAccount :many
SELECT
  account_tags.id,
  account_tags.name,
  account_tags.created_at,
  account_tags.updated_at
FROM juhe_business.account_tag_bindings AS account_tag_bindings
INNER JOIN juhe_business.account_tags AS account_tags
  ON account_tags.id = account_tag_bindings.tag_id
  AND account_tags.system_account_id = account_tag_bindings.system_account_id
WHERE account_tag_bindings.account_id = sqlc.arg(account_id)::text
  AND account_tag_bindings.system_account_id = sqlc.arg(system_account_id)::text
ORDER BY account_tags.name ASC, account_tags.id ASC;
