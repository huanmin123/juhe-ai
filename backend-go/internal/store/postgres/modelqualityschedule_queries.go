package postgres

const modelQualityDatabaseClockCTE = `db_clock AS (
  SELECT
    fixed.now,
    to_char(
      fixed.now AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ) AS now_text
  FROM (SELECT clock_timestamp() AS now) AS fixed
)`

const modelQualityScheduleColumns = `
id, system_account_id, account_id, model, interval_minutes,
profile, penalty_threshold, penalty_action, recovery_interval_minutes,
enabled, revision,
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
  id, system_account_id, account_id, model, interval_minutes,
  profile, penalty_threshold, penalty_action, recovery_interval_minutes,
  enabled, revision, next_run_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $12, $12)
RETURNING ` + modelQualityScheduleColumns

const updateModelQualityScheduleSQL = `
UPDATE juhe_business.model_quality_schedules
SET model = $1,
    interval_minutes = $2,
    profile = $3,
    penalty_threshold = $4,
    penalty_action = $5,
    recovery_interval_minutes = $6,
    enabled = $7,
    revision = revision + 1,
    next_run_at = $8,
    lease_owner = NULL,
    lease_token = NULL,
    lease_until = NULL,
    updated_at = $9
WHERE id = $10 AND revision = $11
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

const claimDueModelQualityScheduleCandidatesSQL = `WITH ` + modelQualityDatabaseClockCTE + `
SELECT
  mqs.id, mqs.system_account_id, mqs.account_id, mqs.model,
  mqs.interval_minutes, mqs.profile, mqs.penalty_threshold, mqs.penalty_action,
  mqs.recovery_interval_minutes, mqs.enabled, mqs.revision, mqs.next_run_at,
  mqs.last_run_id, mqs.last_run_at, mqs.last_run_status,
  mqs.lease_owner, mqs.lease_token, mqs.lease_until,
  mqs.created_at, mqs.updated_at,
  accounts.config_revision
FROM juhe_business.model_quality_schedules AS mqs
JOIN juhe_business.accounts AS accounts
 ON accounts.id = mqs.account_id
 AND accounts.system_account_id = mqs.system_account_id
CROSS JOIN db_clock
WHERE mqs.enabled = 1
  AND mqs.next_run_at <= db_clock.now_text
  AND (mqs.lease_until IS NULL OR mqs.lease_until <= db_clock.now_text)
  AND accounts.deleted_at IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND accounts.status = 'active'
ORDER BY mqs.next_run_at ASC, mqs.id ASC
LIMIT $1
FOR UPDATE OF mqs SKIP LOCKED`

const claimModelQualityScheduleSQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.model_quality_schedules
SET lease_owner = $1,
    lease_token = $2,
    lease_until = to_char(
      (db_clock.now + ($3::bigint * interval '1 millisecond')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    updated_at = db_clock.now_text
FROM db_clock
WHERE id = $4
  AND revision = $5
  AND enabled = 1
  AND next_run_at <= db_clock.now_text
  AND (lease_until IS NULL OR lease_until <= db_clock.now_text)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.accounts AS accounts
    WHERE accounts.id = juhe_business.model_quality_schedules.account_id
      AND accounts.system_account_id = juhe_business.model_quality_schedules.system_account_id
      AND accounts.config_revision = $6
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.status = 'active'
  )
RETURNING lease_until, updated_at`

const completeModelQualityScheduleSQL = `WITH ` + modelQualityDatabaseClockCTE + `
UPDATE juhe_business.model_quality_schedules
SET last_run_id = NULLIF($1, ''),
    last_run_at = db_clock.now_text,
    last_run_status = $2,
    next_run_at = to_char(
      (db_clock.now + ($3::bigint * interval '1 minute')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    lease_owner = NULL,
    lease_token = NULL,
    lease_until = NULL,
    updated_at = db_clock.now_text
FROM db_clock
WHERE id = $4
  AND revision = $5
  AND interval_minutes = $3
  AND lease_owner = $6
  AND lease_token = $7
  AND lease_until = $8
  AND lease_until > db_clock.now_text`
