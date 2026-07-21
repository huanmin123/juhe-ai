package postgres

const lockManagementAccountAuthorizedDispatchTargetSQL = `
SELECT accounts.id, accounts.system_account_id, accounts.name, source_accounts.provider_code,
  source_accounts.type, accounts.status, accounts.schedulable, source_accounts.concurrency_limit,
  group_accounts.local_priority, group_accounts.local_super_priority_enabled,
  group_accounts.local_fallback_enabled, group_accounts.group_id, groups.name,
  accounts.authorization_instance_authorization_id,
  resource_authorizations.status = 'active'
    AND source_accounts.status = 'active'
    AND source_accounts.schedulable = true
    AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > $4)
FROM juhe_business.accounts AS accounts
INNER JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = accounts.authorization_instance_source_account_id AND source_accounts.deleted_at IS NULL
INNER JOIN juhe_business.resource_authorizations AS resource_authorizations
  ON resource_authorizations.id = accounts.authorization_instance_authorization_id
  AND resource_authorizations.resource_type = 'account'
  AND resource_authorizations.resource_id = source_accounts.id
  AND resource_authorizations.grantee_system_account_id = accounts.system_account_id
INNER JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.account_id = accounts.id
  AND group_accounts.system_account_id = accounts.system_account_id
  AND group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id
  AND group_accounts.enabled = true
INNER JOIN juhe_business.groups AS groups ON groups.id = group_accounts.group_id
WHERE accounts.id = $1
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NOT NULL
  AND ($2 OR accounts.system_account_id = $3)
ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
LIMIT 1
FOR UPDATE OF accounts, group_accounts`

const updateManagementAccountAuthorizedDispatchStateSQL = `
UPDATE juhe_business.accounts
SET status = $1, schedulable = $2, cooldown_until = NULL,
  last_error_code = NULL, last_error_message = NULL, last_error_trace_id = NULL,
  cooldown_retest_failure_count = 0, cooldown_retest_observation_started_at = NULL,
  cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
  stream_failure_count = 0, stream_failure_window_started_at = NULL, updated_at = $3
WHERE id = $4 AND system_account_id = $5
  AND authorization_instance_authorization_id = $6 AND deleted_at IS NULL`

const updateManagementAccountAuthorizedDispatchBindingSQL = `
UPDATE juhe_business.group_accounts
SET local_priority = $1, local_super_priority_enabled = $2,
  local_fallback_enabled = $3, updated_at = $4
WHERE account_id = $5 AND system_account_id = $6 AND group_id = $7
  AND account_authorization_id = $8 AND enabled = true`
