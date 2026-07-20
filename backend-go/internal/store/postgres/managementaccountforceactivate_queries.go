package postgres

const forceActivatePendingAccountUpdateSQL = `
UPDATE juhe_business.accounts
SET status = $1,
    schedulable = true,
    cooldown_until = NULL,
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_trace_id = NULL,
    cooldown_retest_failure_count = 0,
    cooldown_retest_observation_started_at = NULL,
    cooldown_retest_last_at = NULL,
    cooldown_retest_last_status_code = NULL,
    next_health_check_at = NULL,
    health_check_failure_count = 0,
    health_check_failure_started_at = NULL,
    last_health_check_error_code = NULL,
    last_health_check_error_message = NULL,
    stream_failure_count = 0,
    stream_failure_window_started_at = NULL,
    updated_at = $2
WHERE id = $3
  AND system_account_id = $4
  AND authorization_instance_authorization_id IS NULL
  AND deleted_at IS NULL
  AND status = 'pending_test'
  AND config_revision = $5
  AND (account_expires_at IS NULL OR account_expires_at > $2)
RETURNING id, system_account_id, status, schedulable`

const forceActivatePendingAccountDirtySQL = `
INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
SELECT DISTINCT group_id, 'account_pending_force_activated', $1
FROM juhe_business.group_accounts
WHERE account_id = $2
ON CONFLICT (group_id) DO UPDATE SET reason = EXCLUDED.reason, updated_at = EXCLUDED.updated_at`
