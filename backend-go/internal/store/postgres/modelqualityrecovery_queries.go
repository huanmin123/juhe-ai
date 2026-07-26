package postgres

const modelQualityEnforcementColumns = `
aqe.account_id, aqe.system_account_id, aqe.enforcement_id, aqe.generation,
aqe.state, aqe.action, aqe.trigger_run_id, aqe.policy_revision,
aqe.account_config_revision, aqe.before_status, aqe.after_status,
aqe.fallback_was_enabled, aqe.super_priority_was_enabled,
aqe.recovery_due_at, aqe.recovery_lease_owner, aqe.recovery_lease_token,
aqe.recovery_lease_until, aqe.last_recovery_run_id, aqe.started_at,
aqe.cleared_at, aqe.updated_at`

const claimDueModelQualityRecoveryCandidatesSQL = `
SELECT ` + modelQualityEnforcementColumns + `,
       COALESCE(NULLIF(mqs.model, ''), NULLIF(accounts.health_check_model, '')) AS recovery_model,
       accounts.config_revision
FROM juhe_business.account_quality_enforcements AS aqe
JOIN juhe_business.accounts AS accounts
  ON accounts.id = aqe.account_id
 AND accounts.system_account_id = aqe.system_account_id
LEFT JOIN juhe_business.model_quality_schedules AS mqs
  ON mqs.account_id = aqe.account_id
 AND mqs.system_account_id = aqe.system_account_id
WHERE aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.recovery_due_at IS NOT NULL
  AND aqe.recovery_due_at <= $1
  AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until <= $1)
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND accounts.status = 'quality_isolated'
ORDER BY aqe.recovery_due_at ASC, aqe.account_id ASC
LIMIT $2
FOR UPDATE OF aqe SKIP LOCKED`

const claimModelQualityRecoverySQL = `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET recovery_lease_owner = $1,
    recovery_lease_token = $2,
    recovery_lease_until = $3,
    account_config_revision = $4,
    updated_at = $5
WHERE aqe.account_id = $6
  AND aqe.enforcement_id = $7
  AND aqe.generation = $8
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.recovery_due_at IS NOT NULL
  AND aqe.recovery_due_at <= $5
  AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until <= $5)
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
  AND (
    ($9 = 0 AND NOT EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
    ))
    OR EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
        AND policies.revision = $9
    )
  )`

const findModelQualityRecoveryScopeSQL = `
SELECT system_account_id
FROM juhe_business.account_quality_enforcements
WHERE account_id = $1
  AND enforcement_id = $2
  AND generation = $3
  AND recovery_lease_owner = $4
  AND recovery_lease_token = $5
  AND recovery_lease_until = $6
  AND recovery_lease_until > $7
LIMIT 1`

const lockModelQualityRecoveryAccountSQL = `
SELECT status, config_revision, availability_schedule_json
FROM juhe_business.accounts
WHERE id = $1
  AND system_account_id = $2
  AND deleted_at IS NULL
LIMIT 1
FOR UPDATE`

const lockModelQualityRecoveryEnforcementSQL = `
SELECT ` + modelQualityEnforcementColumns + `
FROM juhe_business.account_quality_enforcements AS aqe
WHERE aqe.account_id = $1
  AND aqe.enforcement_id = $2
  AND aqe.generation = $3
  AND aqe.recovery_lease_owner = $4
  AND aqe.recovery_lease_token = $5
  AND aqe.recovery_lease_until = $6
  AND aqe.recovery_lease_until > $7
LIMIT 1
FOR UPDATE`

const rescheduleModelQualityRecoverySQL = `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET last_recovery_run_id = $1,
    recovery_due_at = $2,
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    updated_at = $3
WHERE aqe.account_id = $4
  AND aqe.enforcement_id = $5
  AND aqe.generation = $6
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.account_config_revision = $7
  AND aqe.recovery_lease_owner = $8
  AND aqe.recovery_lease_token = $9
  AND aqe.recovery_lease_until = $10
  AND aqe.recovery_lease_until > $3
  AND (
    ($11 = 0 AND NOT EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
    ))
    OR EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
        AND policies.revision = $11
    )
  )`

const recoverModelQualityAccountSQL = `
UPDATE juhe_business.accounts
SET status = $1,
    schedulable = $2,
    last_error_code = NULL,
    last_error_message = NULL,
    config_revision = config_revision + 1,
    updated_at = $3
WHERE id = $4
  AND system_account_id = $5
  AND status = 'quality_isolated'
  AND config_revision = $6
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL`

const clearModelQualityRecoveryEnforcementSQL = `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET state = 'cleared',
    last_recovery_run_id = $1,
    cleared_at = $2,
    recovery_due_at = NULL,
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    updated_at = $2
WHERE aqe.account_id = $3
  AND aqe.enforcement_id = $4
  AND aqe.generation = $5
  AND aqe.state = 'active'
  AND aqe.action = 'quality_isolate'
  AND aqe.account_config_revision = $6
  AND aqe.recovery_lease_owner = $7
  AND aqe.recovery_lease_token = $8
  AND aqe.recovery_lease_until = $9
  AND aqe.recovery_lease_until > $2
  AND (
    ($10 = 0 AND NOT EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
    ))
    OR EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = aqe.system_account_id
        AND policies.revision = $10
    )
  )`
