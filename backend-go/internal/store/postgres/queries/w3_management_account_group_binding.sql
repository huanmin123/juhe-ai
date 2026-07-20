-- name: LockManagementAccountGroupBindingTarget :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.client_compatibility,
  accounts.schedulable,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.super_priority_enabled,
  accounts.fallback_enabled,
  accounts.health_check_model,
  groups.id AS group_id,
  groups.name AS group_name,
  COALESCE(previous_binding.group_id, '') AS previous_group_id
FROM juhe_business.groups AS groups
INNER JOIN juhe_business.accounts AS accounts
  ON accounts.id = sqlc.arg(account_id)::text
  AND accounts.system_account_id = groups.system_account_id
  AND accounts.provider_code = groups.provider_code
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
LEFT JOIN LATERAL (
  SELECT group_accounts.group_id
  FROM juhe_business.group_accounts AS group_accounts
  WHERE group_accounts.account_id = accounts.id
    AND group_accounts.system_account_id = accounts.system_account_id
    AND group_accounts.enabled = true
  ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
  LIMIT 1
) AS previous_binding ON true
WHERE groups.id = sqlc.arg(group_id)::text
  AND (
    sqlc.arg(can_access_all)::boolean
    OR groups.system_account_id = sqlc.arg(effective_system_account_id)::text
  )
FOR UPDATE OF groups, accounts;

-- name: DeleteManagementAccountGroupBindings :exec
DELETE FROM juhe_business.group_accounts
WHERE account_id = sqlc.arg(account_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text;

-- name: UpsertManagementAccountGroupBinding :exec
INSERT INTO juhe_business.group_accounts (
  system_account_id,
  group_id,
  account_id,
  account_authorization_id,
  local_priority,
  local_super_priority_enabled,
  local_fallback_enabled,
  enabled,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(system_account_id)::text,
  sqlc.arg(group_id)::text,
  sqlc.arg(account_id)::text,
  NULL,
  sqlc.arg(local_priority)::int,
  sqlc.arg(local_super_priority_enabled)::boolean,
  sqlc.arg(local_fallback_enabled)::boolean,
  true,
  sqlc.arg(updated_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
ON CONFLICT (group_id, account_id) DO UPDATE SET
  system_account_id = EXCLUDED.system_account_id,
  account_authorization_id = NULL,
  local_priority = EXCLUDED.local_priority,
  local_super_priority_enabled = EXCLUDED.local_super_priority_enabled,
  local_fallback_enabled = EXCLUDED.local_fallback_enabled,
  enabled = true,
  updated_at = EXCLUDED.updated_at;
