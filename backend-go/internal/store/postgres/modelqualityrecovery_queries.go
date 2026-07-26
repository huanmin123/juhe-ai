package postgres

const modelQualityEnforcementColumns = `
aqe.account_id, aqe.system_account_id, aqe.enforcement_id, aqe.generation,
aqe.state, aqe.action, aqe.trigger_run_id, aqe.config_source,
aqe.config_source_id, aqe.policy_revision, aqe.profile, aqe.penalty_threshold,
aqe.recovery_interval_minutes, aqe.recovery_model,
aqe.account_config_revision, aqe.before_status, aqe.after_status,
aqe.fallback_was_enabled, aqe.super_priority_was_enabled,
aqe.recovery_due_at, aqe.recovery_lease_owner, aqe.recovery_lease_token,
aqe.recovery_lease_until, aqe.last_recovery_run_id, aqe.started_at,
aqe.cleared_at, aqe.updated_at`

const claimDueModelQualityRecoveryCandidatesSQL = `WITH ` + modelQualityDatabaseClockCTE + `
SELECT ` + modelQualityEnforcementColumns + `,
       COALESCE(NULLIF(aqe.recovery_model, ''), NULLIF(accounts.health_check_model, '')) AS effective_recovery_model,
       accounts.config_revision
FROM juhe_business.account_quality_enforcements AS aqe
JOIN juhe_business.accounts AS accounts
  ON accounts.id = aqe.account_id
 AND accounts.system_account_id = aqe.system_account_id
CROSS JOIN db_clock
WHERE aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.recovery_due_at IS NOT NULL
  AND aqe.recovery_due_at <= db_clock.now_text
  AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until <= db_clock.now_text)
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND accounts.status = 'quality_isolated'
ORDER BY aqe.recovery_due_at ASC, aqe.account_id ASC
LIMIT $1
FOR UPDATE OF aqe SKIP LOCKED`

const claimModelQualityRecoverySQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET recovery_lease_owner = $1,
    recovery_lease_token = $2,
    recovery_lease_until = to_char(
      (db_clock.now + ($3::bigint * interval '1 millisecond')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    account_config_revision = $4,
    updated_at = db_clock.now_text
FROM db_clock
WHERE aqe.account_id = $5
  AND aqe.enforcement_id = $6
  AND aqe.generation = $7
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.recovery_due_at IS NOT NULL
  AND aqe.recovery_due_at <= db_clock.now_text
  AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until <= db_clock.now_text)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.accounts AS accounts
    WHERE accounts.id = aqe.account_id
      AND accounts.system_account_id = aqe.system_account_id
      AND accounts.config_revision = $4
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.status = 'quality_isolated'
  )
RETURNING aqe.recovery_lease_until, aqe.updated_at`

const findModelQualityRecoveryScopeSQL = `WITH ` + modelQualityDatabaseClockCTE + `
SELECT aqe.system_account_id
FROM juhe_business.account_quality_enforcements AS aqe
CROSS JOIN db_clock
WHERE aqe.account_id = $1
  AND aqe.enforcement_id = $2
  AND aqe.generation = $3
  AND aqe.recovery_lease_owner = $4
  AND aqe.recovery_lease_token = $5
  AND aqe.recovery_lease_until = $6
  AND aqe.recovery_lease_until > db_clock.now_text
LIMIT 1`

const lockModelQualityRecoveryAccountSQL = `
SELECT status, config_revision, availability_schedule_json
FROM juhe_business.accounts
WHERE id = $1
  AND system_account_id = $2
  AND deleted_at IS NULL
LIMIT 1
FOR UPDATE`

const lockModelQualityRecoveryEnforcementSQL = `WITH ` + modelQualityDatabaseClockCTE + `
SELECT ` + modelQualityEnforcementColumns + `, db_clock.now_text
FROM juhe_business.account_quality_enforcements AS aqe
CROSS JOIN db_clock
WHERE aqe.account_id = $1
  AND aqe.enforcement_id = $2
  AND aqe.generation = $3
  AND aqe.recovery_lease_owner = $4
  AND aqe.recovery_lease_token = $5
  AND aqe.recovery_lease_until = $6
  AND aqe.recovery_lease_until > db_clock.now_text
LIMIT 1
FOR UPDATE OF aqe`

const rescheduleModelQualityRecoverySQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET last_recovery_run_id = $1,
    recovery_due_at = to_char(
      (db_clock.now + ($2::bigint * interval '1 minute')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    updated_at = db_clock.now_text
FROM db_clock
WHERE aqe.account_id = $3
  AND aqe.enforcement_id = $4
  AND aqe.generation = $5
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.account_config_revision = $6
  AND aqe.recovery_lease_owner = $7
  AND aqe.recovery_lease_token = $8
  AND aqe.recovery_lease_until = $9
  AND aqe.recovery_lease_until > db_clock.now_text
RETURNING aqe.recovery_due_at`

const recoverModelQualityAccountSQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.accounts
SET status = $1,
    schedulable = $2,
    last_error_code = NULL,
    last_error_message = NULL,
    config_revision = config_revision + 1,
    updated_at = db_clock.now_text
FROM db_clock
WHERE id = $3
  AND system_account_id = $4
  AND status = 'quality_isolated'
  AND config_revision = $5
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL`

const clearModelQualityRecoveryEnforcementSQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET state = 'cleared',
    last_recovery_run_id = $1,
    cleared_at = db_clock.now_text,
    recovery_due_at = NULL,
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    updated_at = db_clock.now_text
FROM db_clock
WHERE aqe.account_id = $2
  AND aqe.enforcement_id = $3
  AND aqe.generation = $4
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.account_config_revision = $5
  AND aqe.recovery_lease_owner = $6
  AND aqe.recovery_lease_token = $7
  AND aqe.recovery_lease_until = $8
  AND aqe.recovery_lease_until > db_clock.now_text
`
