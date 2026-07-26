package postgres

const modelQualityScheduleColumns = `
id, system_account_id, account_id, model, interval_minutes, enabled, revision,
next_run_at, last_run_id, last_run_at, last_run_status,
lease_owner, lease_token, lease_until, created_at, updated_at`

const lockModelQualityScheduleAccountSQL = `
SELECT accounts.id
FROM juhe_business.accounts AS accounts
WHERE accounts.id = $1
  AND accounts.system_account_id = $2
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
LIMIT 1
FOR UPDATE OF accounts`

const lockModelQualityScheduleByScopeSQL = `
SELECT ` + modelQualityScheduleColumns + `
FROM juhe_business.model_quality_schedules
WHERE system_account_id = $1 AND account_id = $2
LIMIT 1
FOR UPDATE`

const insertModelQualityScheduleSQL = `
INSERT INTO juhe_business.model_quality_schedules (
  id, system_account_id, account_id, model, interval_minutes, enabled, revision,
  next_run_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $8)
RETURNING ` + modelQualityScheduleColumns

const updateModelQualityScheduleSQL = `
UPDATE juhe_business.model_quality_schedules
SET model = $1,
    interval_minutes = $2,
    enabled = $3,
    revision = revision + 1,
    next_run_at = $4,
    lease_owner = NULL,
    lease_token = NULL,
    lease_until = NULL,
    updated_at = $5
WHERE id = $6 AND revision = $7
RETURNING ` + modelQualityScheduleColumns

const lockModelQualityScheduleDeleteSQL = `
SELECT ` + modelQualityScheduleColumns + `
FROM juhe_business.model_quality_schedules
WHERE id = $1 AND system_account_id = $2
LIMIT 1
FOR UPDATE`

const deleteModelQualityScheduleSQL = `
DELETE FROM juhe_business.model_quality_schedules
WHERE id = $1 AND system_account_id = $2 AND revision = $3`

const claimDueModelQualityScheduleCandidatesSQL = `
SELECT
  mqs.id, mqs.system_account_id, mqs.account_id, mqs.model,
  mqs.interval_minutes, mqs.enabled, mqs.revision, mqs.next_run_at,
  mqs.last_run_id, mqs.last_run_at, mqs.last_run_status,
  mqs.lease_owner, mqs.lease_token, mqs.lease_until,
  mqs.created_at, mqs.updated_at,
  accounts.config_revision
FROM juhe_business.model_quality_schedules AS mqs
JOIN juhe_business.accounts AS accounts
  ON accounts.id = mqs.account_id
 AND accounts.system_account_id = mqs.system_account_id
WHERE mqs.enabled = 1
  AND mqs.next_run_at <= $1
  AND (mqs.lease_until IS NULL OR mqs.lease_until <= $1)
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND accounts.status = 'active'
ORDER BY mqs.next_run_at ASC, mqs.id ASC
LIMIT $2
FOR UPDATE OF mqs SKIP LOCKED`

const claimModelQualityScheduleSQL = `
UPDATE juhe_business.model_quality_schedules
SET lease_owner = $1,
    lease_token = $2,
    lease_until = $3,
    updated_at = $4
WHERE id = $5
  AND revision = $6
  AND enabled = 1
  AND next_run_at <= $4
  AND (lease_until IS NULL OR lease_until <= $4)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.accounts AS accounts
    WHERE accounts.id = juhe_business.model_quality_schedules.account_id
      AND accounts.system_account_id = juhe_business.model_quality_schedules.system_account_id
      AND accounts.config_revision = $7
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.status = 'active'
  )`

const completeModelQualityScheduleSQL = `
UPDATE juhe_business.model_quality_schedules
SET last_run_id = NULLIF($1, ''),
    last_run_at = $2,
    last_run_status = $3,
    next_run_at = $4,
    lease_owner = NULL,
    lease_token = NULL,
    lease_until = NULL,
    updated_at = $2
WHERE id = $5
  AND revision = $6
  AND interval_minutes = $7
  AND lease_owner = $8
  AND lease_token = $9
  AND lease_until = $10
  AND updated_at <= $2
  AND lease_until > $2`
