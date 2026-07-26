package postgres

const modelQualityHealthSyncDatabaseNowExpression = `
to_char(
  clock_timestamp() AT TIME ZONE 'UTC',
  'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
)`

const claimModelQualityHealthSyncCandidatesSQL = `
SELECT
  runs.id,
  runs.system_account_id,
  runs.provider_code,
  runs.account_id,
  runs.model,
  runs.profile,
  runs.score,
  runs.level,
  runs.finished_at,
  CASE
    WHEN octet_length(runs.quality_decision_json) <= $2
      THEN runs.quality_decision_json
    ELSE NULL
  END AS bounded_quality_decision_json,
  COALESCE(octet_length(runs.quality_decision_json), 0) AS quality_decision_bytes,
  runs.updated_at,
  runs.quality_health_sync_claim_epoch,
  runs.quality_health_sync_attempt_count
FROM juhe_dataset.model_check_runs AS runs
WHERE runs.account_id IS NOT NULL
  AND runs.status = 'completed'
  AND runs.quality_health_sync_status = 'failed'
  AND COALESCE(runs.quality_health_sync_next_attempt_at, runs.updated_at) <= ` + modelQualityHealthSyncDatabaseNowExpression + `
  AND (
    runs.quality_health_sync_claim_until IS NULL
    OR runs.quality_health_sync_claim_until <= ` + modelQualityHealthSyncDatabaseNowExpression + `
  )
  AND runs.quality_health_sync_claim_epoch < 9223372036854775807
  AND runs.quality_health_sync_attempt_count < 9223372036854775807
ORDER BY
  COALESCE(runs.quality_health_sync_next_attempt_at, runs.updated_at) ASC,
  runs.updated_at ASC,
  runs.id ASC
LIMIT $1
FOR UPDATE OF runs SKIP LOCKED`

const claimModelQualityHealthSyncRunSQL = `
WITH db_clock AS (
  SELECT
    to_char(fixed.now AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS now_text,
    to_char(
      (fixed.now + ($3::bigint * interval '1 millisecond')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ) AS lease_until_text
  FROM (SELECT clock_timestamp() AS now) AS fixed
)
UPDATE juhe_dataset.model_check_runs
SET quality_health_sync_claim_owner = $1,
    quality_health_sync_claim_token = $2,
    quality_health_sync_claim_epoch = quality_health_sync_claim_epoch + 1,
    quality_health_sync_claim_until = db_clock.lease_until_text,
    quality_health_sync_next_attempt_at = db_clock.lease_until_text,
    quality_health_sync_attempt_count = quality_health_sync_attempt_count + 1,
    quality_health_sync_last_error_class = NULL,
    quality_health_sync_last_error_message = NULL,
    quality_health_sync_updated_at = db_clock.now_text
FROM db_clock
WHERE id = $4
  AND status = 'completed'
  AND quality_health_sync_status = 'failed'
  AND quality_health_sync_claim_epoch = $5
  AND quality_health_sync_attempt_count = $6
  AND (
    quality_health_sync_claim_until IS NULL
    OR quality_health_sync_claim_until <= db_clock.now_text
  )
RETURNING
  quality_health_sync_claim_epoch,
  quality_health_sync_claim_until,
  quality_health_sync_updated_at`

const quarantineModelQualityHealthSyncRunSQL = `
UPDATE juhe_dataset.model_check_runs
SET quality_health_sync_claim_owner = NULL,
    quality_health_sync_claim_token = NULL,
    quality_health_sync_claim_until = NULL,
    quality_health_sync_next_attempt_at = to_char(
      (clock_timestamp() + ($1::bigint * interval '1 millisecond')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    quality_health_sync_attempt_count = quality_health_sync_attempt_count + 1,
    quality_health_sync_last_error_class = $2,
    quality_health_sync_last_error_message = $3,
    quality_health_sync_updated_at = ` + modelQualityHealthSyncDatabaseNowExpression + `
WHERE id = $4
  AND status = 'completed'
  AND quality_health_sync_status = 'failed'
  AND quality_health_sync_claim_epoch = $5
  AND quality_health_sync_attempt_count = $6`

const lockModelQualityHealthSyncAccountSQL = `
SELECT accounts.id
FROM juhe_business.accounts AS accounts
WHERE accounts.id = $1
  AND accounts.system_account_id = $2
  AND accounts.deleted_at IS NULL
LIMIT 1
FOR KEY SHARE OF accounts`

const completeModelQualityHealthSyncRunSQL = `
UPDATE juhe_dataset.model_check_runs
SET quality_decision_json = $1,
    quality_health_sync_status = 'applied',
    quality_health_sync_claim_owner = NULL,
    quality_health_sync_claim_token = NULL,
    quality_health_sync_claim_until = NULL,
    quality_health_sync_next_attempt_at = NULL,
    quality_health_sync_last_error_class = NULL,
    quality_health_sync_last_error_message = NULL,
    quality_health_sync_updated_at = $2,
    updated_at = $2
WHERE id = $3
  AND account_id = $4
  AND system_account_id = $5
  AND status = 'completed'
  AND quality_health_sync_status = 'failed'
  AND quality_decision_json = $6
  AND updated_at = $7
  AND quality_health_sync_claim_owner = $8
  AND quality_health_sync_claim_token = $9
  AND quality_health_sync_claim_epoch = $10
  AND quality_health_sync_claim_until = $11
  AND quality_health_sync_claim_until > ` + modelQualityHealthSyncDatabaseNowExpression

const releaseModelQualityHealthSyncRunSQL = `
UPDATE juhe_dataset.model_check_runs
SET quality_health_sync_claim_owner = NULL,
    quality_health_sync_claim_token = NULL,
    quality_health_sync_claim_until = NULL,
    quality_health_sync_next_attempt_at = to_char(
      (clock_timestamp() + ($1::bigint * interval '1 millisecond')) AT TIME ZONE 'UTC',
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    ),
    quality_health_sync_last_error_class = $2,
    quality_health_sync_last_error_message = $3,
    quality_health_sync_updated_at = ` + modelQualityHealthSyncDatabaseNowExpression + `
WHERE id = $4
  AND status = 'completed'
  AND quality_health_sync_status = 'failed'
  AND quality_health_sync_claim_owner = $5
  AND quality_health_sync_claim_token = $6
  AND quality_health_sync_claim_epoch = $7
  AND quality_health_sync_claim_until = $8
  AND quality_health_sync_claim_until > ` + modelQualityHealthSyncDatabaseNowExpression
