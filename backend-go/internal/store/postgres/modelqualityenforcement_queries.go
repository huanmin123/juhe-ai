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

const lockModelQualityEnforcementSQL = `
SELECT ` + modelQualityEnforcementColumns + `
FROM juhe_business.account_quality_enforcements AS aqe
WHERE aqe.account_id = $1
LIMIT 1
FOR UPDATE`

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
  AND (
    ($9 = 0 AND $2 = 'fallback' AND NOT EXISTS (
      SELECT 1
      FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = accounts.system_account_id
    ))
    OR EXISTS (
      SELECT 1
      FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = accounts.system_account_id
        AND policies.revision = $9
        AND policies.penalty_action = $2
        AND ($10 <> 'manual' OR policies.manual_enforcement_enabled = 1)
    )
  )`

const insertModelQualityEnforcementSQL = `
INSERT INTO juhe_business.account_quality_enforcements AS aqe (
  account_id, system_account_id, enforcement_id, generation, state, action,
  trigger_run_id, policy_revision, account_config_revision,
  before_status, after_status, fallback_was_enabled,
  super_priority_was_enabled, started_at, recovery_due_at,
  recovery_lease_owner, recovery_lease_token, recovery_lease_until,
  last_recovery_run_id, cleared_at, created_at, updated_at
)
SELECT
  $1, $2, $3, $4, 'active', $5,
  $6, $7, $8, $9, $10, $11,
  $12, $13, $14,
  NULL, NULL, NULL,
  NULL, NULL, $13, $13
WHERE EXISTS (
  SELECT 1
  FROM juhe_business.accounts AS accounts
  WHERE accounts.id = $1
    AND accounts.system_account_id = $2
    AND accounts.status = $10
    AND accounts.config_revision = $15
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
)
AND (
  ($7 = 0 AND $5 = 'fallback' AND NOT EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_policies AS policies
    WHERE policies.system_account_id = $2
  ))
  OR EXISTS (
    SELECT 1
    FROM juhe_business.model_quality_policies AS policies
    WHERE policies.system_account_id = $2
      AND policies.revision = $7
      AND policies.penalty_action = $5
      AND ($16 <> 'manual' OR policies.manual_enforcement_enabled = 1)
  )
)
ON CONFLICT (account_id) DO NOTHING
RETURNING ` + modelQualityEnforcementColumns

const replaceModelQualityEnforcementSQL = `
UPDATE juhe_business.account_quality_enforcements AS aqe
SET system_account_id = $2,
    enforcement_id = $3,
    generation = $4,
    state = 'active',
    action = $5,
    trigger_run_id = $6,
    policy_revision = $7,
    account_config_revision = $8,
    before_status = $9,
    after_status = $10,
    fallback_was_enabled = $11,
    super_priority_was_enabled = $12,
    started_at = $13,
    recovery_due_at = $14,
    recovery_lease_owner = NULL,
    recovery_lease_token = NULL,
    recovery_lease_until = NULL,
    last_recovery_run_id = NULL,
    cleared_at = NULL,
    updated_at = $13
WHERE aqe.account_id = $1
  AND aqe.enforcement_id = $17
  AND aqe.generation = $18
  AND EXISTS (
    SELECT 1
    FROM juhe_business.accounts AS accounts
    WHERE accounts.id = aqe.account_id
      AND accounts.system_account_id = $2
      AND accounts.status = $10
      AND accounts.config_revision = $15
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
  )
  AND (
    ($7 = 0 AND $5 = 'fallback' AND NOT EXISTS (
      SELECT 1
      FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = $2
    ))
    OR EXISTS (
      SELECT 1
      FROM juhe_business.model_quality_policies AS policies
      WHERE policies.system_account_id = $2
        AND policies.revision = $7
        AND policies.penalty_action = $5
        AND ($16 <> 'manual' OR policies.manual_enforcement_enabled = 1)
    )
  )
RETURNING ` + modelQualityEnforcementColumns
