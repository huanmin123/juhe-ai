package postgres

const lockManagementAccountTrafficMigrationAccountSQL = `
SELECT accounts.id, accounts.system_account_id,
  COALESCE(source_accounts.system_account_id, accounts.system_account_id), accounts.name,
  COALESCE(source_accounts.provider_code, accounts.provider_code), COALESCE(source_accounts.type, accounts.type),
  accounts.status, accounts.schedulable, accounts.cooldown_until, group_accounts.group_id,
  accounts.authorization_instance_authorization_id,
  CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN 'owner' ELSE 'authorized' END,
  CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN
    accounts.status = 'active' AND accounts.schedulable = true
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > $4)
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= $4)
  ELSE resource_authorizations.status = 'active'
    AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $4)
    AND source_accounts.status = 'active' AND source_accounts.schedulable = true
    AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > $4)
    AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= $4)
    AND accounts.status = 'active' AND accounts.schedulable = true
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > $4)
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= $4)
  END
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = accounts.authorization_instance_source_account_id AND source_accounts.deleted_at IS NULL
LEFT JOIN juhe_business.resource_authorizations AS resource_authorizations
  ON resource_authorizations.id = accounts.authorization_instance_authorization_id
  AND resource_authorizations.resource_type = 'account'
  AND resource_authorizations.resource_id = source_accounts.id
  AND resource_authorizations.resource_owner_system_account_id = source_accounts.system_account_id
  AND resource_authorizations.grantee_system_account_id = accounts.system_account_id
INNER JOIN LATERAL (
  SELECT bindings.group_id FROM juhe_business.group_accounts AS bindings
  WHERE bindings.account_id = accounts.id AND bindings.system_account_id = accounts.system_account_id
    AND bindings.enabled = true
    AND bindings.account_authorization_id IS NOT DISTINCT FROM accounts.authorization_instance_authorization_id
  ORDER BY bindings.updated_at DESC, bindings.group_id ASC LIMIT 1
) AS group_accounts ON true
WHERE accounts.id = $1 AND accounts.deleted_at IS NULL
  AND ($2 OR accounts.system_account_id = $3)
  AND ((accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL)
    OR (accounts.authorization_instance_authorization_id IS NOT NULL
      AND source_accounts.id IS NOT NULL AND resource_authorizations.id IS NOT NULL
      AND resource_authorizations.status = 'active'
      AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $4)))
FOR UPDATE OF accounts`

const updateManagementAccountTrafficMigrationSourceSQL = `
UPDATE juhe_business.accounts
SET status = $1, schedulable = $2, cooldown_until = $3,
  last_error_code = NULL, last_error_message = '手动迁移流量', last_error_trace_id = NULL,
  cooldown_retest_failure_count = 0, cooldown_retest_observation_started_at = $4,
  cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
  stream_failure_count = 0, stream_failure_window_started_at = NULL, updated_at = $5
WHERE id = $6 AND system_account_id = $7
  AND authorization_instance_authorization_id IS NOT DISTINCT FROM $8
  AND deleted_at IS NULL`

const markManagementAccountTrafficMigrationStatsDirtySQL = `
INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
VALUES ($1, 'traffic_migration', $2)
ON CONFLICT (group_id) DO UPDATE SET reason = EXCLUDED.reason, updated_at = EXCLUDED.updated_at`
