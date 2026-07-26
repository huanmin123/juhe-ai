package postgres

const lockModelQualityEnforcementAccountSQL = `
SELECT
  accounts.system_account_id,
  accounts.status,
  accounts.config_revision,
  accounts.fallback_enabled,
  accounts.super_priority_enabled,
  accounts.deleted_at IS NULL AS not_deleted,
  accounts.authorization_instance_authorization_id IS NULL AS own_physical
FROM juhe_business.accounts AS accounts
WHERE accounts.id = $1
LIMIT 1
FOR UPDATE OF accounts`

const lockModelQualityEnforcementScheduleSQL = `
SELECT ` + modelQualityScheduleColumns + `
FROM juhe_business.model_quality_schedules
WHERE id = $1 AND system_account_id = $2 AND account_id = $3
LIMIT 1
FOR SHARE`

const lockModelQualityEnforcementSQL = `
SELECT ` + modelQualityEnforcementColumns + `
FROM juhe_business.account_quality_enforcements AS aqe
WHERE aqe.account_id = $1
LIMIT 1
FOR UPDATE`

// modelQualityEnforcementConfigFenceSQL is shared by the account mutation and
// generation write. Manual runs fence the current manual policy; scheduled
// runs fence the schedule-owned snapshot, including its model.
const modelQualityEnforcementConfigFenceSQL = `(
  ($10 = 'manual' AND $11 IS NULL AND EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_policies AS policies
    WHERE policies.system_account_id = accounts.system_account_id
      AND policies.revision = $9
      AND policies.profile = $12
      AND policies.penalty_threshold = $13
      AND policies.penalty_action = $2
      AND policies.recovery_interval_minutes = $14
      AND ($16 <> 'manual' OR policies.manual_enforcement_enabled = 1)
  ))
  OR
  ($10 = 'manual' AND $11 IS NULL AND $9 = 0 AND $12 = 'quick'
    AND $13 = 70 AND $2 = 'fallback' AND $14 = 10 AND $16 = 'manual'
    AND NOT EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = accounts.system_account_id
    ))
  OR
  ($10 = 'schedule' AND EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_schedules AS schedules
    WHERE schedules.id = $11
      AND schedules.system_account_id = accounts.system_account_id
      AND schedules.account_id = accounts.id
      AND schedules.revision = $9
      AND schedules.profile = $12
      AND schedules.penalty_threshold = $13
      AND schedules.penalty_action = $2
      AND schedules.recovery_interval_minutes = $14
      AND schedules.model = $15
  ))
)`

const updateModelQualityEnforcementAccountSQL = `
UPDATE juhe_business.accounts AS accounts
SET status = $1,
    schedulable = CASE
      WHEN $2 IN ('disable', 'quality_isolate') THEN false
      ELSE accounts.schedulable
    END,
    fallback_enabled = CASE
      WHEN $2 = 'fallback' THEN true
      ELSE accounts.fallback_enabled
    END,
    super_priority_enabled = CASE
      WHEN $2 = 'fallback' THEN false
      ELSE accounts.super_priority_enabled
    END,
    last_error_code = 'model_quality_failed',
    last_error_message = $3,
    last_error_trace_id = NULL,
    config_revision = accounts.config_revision + 1,
    updated_at = $4
WHERE accounts.id = $5
  AND accounts.system_account_id = $6
  AND accounts.status = $7
  AND accounts.config_revision = $8
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND ` + modelQualityEnforcementConfigFenceSQL

const insertModelQualityEnforcementSQL = `
INSERT INTO juhe_business.account_quality_enforcements AS aqe (
  account_id, system_account_id, enforcement_id, generation, state, action,
  trigger_run_id, config_source, config_source_id, policy_revision,
  profile, penalty_threshold, recovery_interval_minutes, recovery_model,
  account_config_revision, before_status, after_status, fallback_was_enabled,
  super_priority_was_enabled, started_at, recovery_due_at,
  recovery_lease_owner, recovery_lease_token, recovery_lease_until,
  last_recovery_run_id, cleared_at, created_at, updated_at
)
SELECT
  $1, $2, $3, $4, 'active', $5,
  $6, $7, $8, $9,
  $10, $11, $12, $13,
  $14, $15, $16, $17,
  $18, $19, $20,
  NULL, NULL, NULL,
  NULL, NULL, $19, $19
WHERE EXISTS (
  SELECT 1
  FROM juhe_business.accounts AS accounts
  WHERE accounts.id = $1
    AND accounts.system_account_id = $2
    AND accounts.status = $16
    AND accounts.config_revision = $21
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
    AND ` + modelQualityEnforcementWriteConfigFenceSQL + `
)
ON CONFLICT (account_id) DO NOTHING
RETURNING ` + modelQualityEnforcementColumns

// The write form uses the write-argument positions while preserving the same
// manual/schedule snapshot checks as the account mutation.
const modelQualityEnforcementWriteConfigFenceSQL = `(
  ($7 = 'manual' AND $8 IS NULL AND EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_policies AS policies
    WHERE policies.system_account_id = accounts.system_account_id
      AND policies.revision = $9
      AND policies.profile = $10
      AND policies.penalty_threshold = $11
      AND policies.penalty_action = $5
      AND policies.recovery_interval_minutes = $12
      AND ($22 <> 'manual' OR policies.manual_enforcement_enabled = 1)
  ))
  OR
  ($7 = 'manual' AND $8 IS NULL AND $9 = 0 AND $10 = 'quick'
    AND $11 = 70 AND $5 = 'fallback' AND $12 = 10 AND $22 = 'manual'
    AND NOT EXISTS (
      SELECT 1 FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = accounts.system_account_id
    ))
  OR
  ($7 = 'schedule' AND EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_schedules AS schedules
    WHERE schedules.id = $8
      AND schedules.system_account_id = accounts.system_account_id
      AND schedules.account_id = accounts.id
      AND schedules.revision = $9
      AND schedules.profile = $10
      AND schedules.penalty_threshold = $11
      AND schedules.penalty_action = $5
      AND schedules.recovery_interval_minutes = $12
      AND schedules.model = $13
  ))
)`

const replaceModelQualityEnforcementSQL = `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET system_account_id = $2,
    enforcement_id = $3,
    generation = $4,
    state = 'active',
    action = $5,
    trigger_run_id = $6,
    config_source = $7,
    config_source_id = $8,
    policy_revision = $9,
    profile = $10,
    penalty_threshold = $11,
    recovery_interval_minutes = $12,
    recovery_model = $13,
    account_config_revision = $14,
    before_status = $15,
    after_status = $16,
    fallback_was_enabled = $17,
    super_priority_was_enabled = $18,
    started_at = $19,
    recovery_due_at = $20,
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    last_recovery_run_id = NULL,
    cleared_at = NULL,
    updated_at = $19
WHERE aqe.account_id = $1
  AND aqe.enforcement_id = $23
  AND aqe.generation = $24
  AND EXISTS (
    SELECT 1
    FROM juhe_business.accounts AS accounts
    WHERE accounts.id = aqe.account_id
      AND accounts.system_account_id = $2
      AND accounts.status = $16
      AND accounts.config_revision = $21
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND ` + modelQualityEnforcementWriteConfigFenceSQL + `
  )
RETURNING ` + modelQualityEnforcementColumns
