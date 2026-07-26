package postgres

const modelQualityHealthSyncDatabaseNowExpression = `
to_char(
  clock_timestamp() AT TIME ZONE 'UTC',
  'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
)`

const modelQualityHealthSyncCanonicalTimestampRegex = `^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$`

const claimModelQualityHealthSyncCandidatesSQL = `
SELECT
  runs.ctid::text AS row_ref,
  CASE WHEN octet_length(runs.id) <= $3 THEN runs.id ELSE NULL END AS bounded_id,
  COALESCE(octet_length(runs.id), 0) AS id_bytes,
  CASE WHEN octet_length(runs.system_account_id) <= $4 THEN runs.system_account_id ELSE NULL END AS bounded_system_account_id,
  COALESCE(octet_length(runs.system_account_id), 0) AS system_account_id_bytes,
  CASE WHEN octet_length(runs.provider_code) <= $5 THEN runs.provider_code ELSE NULL END AS bounded_provider_code,
  COALESCE(octet_length(runs.provider_code), 0) AS provider_code_bytes,
  CASE WHEN octet_length(runs.account_id) <= $6 THEN runs.account_id ELSE NULL END AS bounded_account_id,
  COALESCE(octet_length(runs.account_id), 0) AS account_id_bytes,
  CASE WHEN octet_length(runs.model) <= $7 THEN runs.model ELSE NULL END AS bounded_model,
  COALESCE(octet_length(runs.model), 0) AS model_bytes,
  CASE WHEN octet_length(runs.profile) <= $8 THEN runs.profile ELSE NULL END AS bounded_profile,
  COALESCE(octet_length(runs.profile), 0) AS profile_bytes,
  runs.score,
  CASE WHEN octet_length(runs.level) <= $9 THEN runs.level ELSE NULL END AS bounded_level,
  COALESCE(octet_length(runs.level), 0) AS level_bytes,
  CASE WHEN octet_length(runs.finished_at) <= $10 THEN runs.finished_at ELSE NULL END AS bounded_finished_at,
  COALESCE(octet_length(runs.finished_at), 0) AS finished_at_bytes,
  CASE
    WHEN octet_length(runs.quality_decision_json) <= $2
      THEN runs.quality_decision_json
    ELSE NULL
  END AS bounded_quality_decision_json,
  COALESCE(octet_length(runs.quality_decision_json), 0) AS quality_decision_bytes,
  CASE WHEN octet_length(runs.updated_at) <= $11 THEN runs.updated_at ELSE NULL END AS bounded_updated_at,
  COALESCE(octet_length(runs.updated_at), 0) AS updated_at_bytes,
  runs.quality_health_sync_claim_epoch,
  runs.quality_health_sync_attempt_count
FROM juhe_dataset.model_check_runs AS runs
WHERE runs.account_id IS NOT NULL
  AND runs.status = 'completed'
  AND runs.quality_health_sync_status = 'failed'
  AND CASE
    WHEN runs.quality_health_sync_claim_until IS NOT NULL
      AND runs.quality_health_sync_claim_until !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN TRUE
    WHEN runs.updated_at IS NULL OR runs.updated_at !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN TRUE
    WHEN runs.quality_health_sync_next_attempt_at IS NULL THEN runs.updated_at <= ` + modelQualityHealthSyncDatabaseNowExpression + `
    WHEN runs.quality_health_sync_next_attempt_at !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN TRUE
    ELSE runs.quality_health_sync_next_attempt_at <= ` + modelQualityHealthSyncDatabaseNowExpression + `
  END
  AND CASE
    WHEN runs.quality_health_sync_claim_until IS NULL THEN TRUE
    WHEN runs.quality_health_sync_claim_until !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN TRUE
    ELSE runs.quality_health_sync_claim_until <= ` + modelQualityHealthSyncDatabaseNowExpression + `
  END
  AND runs.quality_health_sync_claim_epoch < 9223372036854775807
  AND runs.quality_health_sync_attempt_count < 9223372036854775807
ORDER BY
  CASE
    WHEN runs.updated_at IS NULL OR runs.updated_at !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `'
      OR (runs.quality_health_sync_next_attempt_at IS NOT NULL AND runs.quality_health_sync_next_attempt_at !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `')
      OR (runs.quality_health_sync_claim_until IS NOT NULL AND runs.quality_health_sync_claim_until !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `')
      THEN 0
    ELSE 1
  END ASC,
  CASE
    WHEN runs.quality_health_sync_next_attempt_at ~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN runs.quality_health_sync_next_attempt_at
    WHEN runs.updated_at ~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN runs.updated_at
    ELSE NULL
  END ASC NULLS FIRST,
  CASE WHEN runs.updated_at ~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN runs.updated_at ELSE NULL END ASC NULLS FIRST,
  CASE WHEN octet_length(runs.id) <= $3 THEN runs.id ELSE NULL END ASC NULLS FIRST,
  runs.ctid ASC
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
  AND CASE
    WHEN quality_health_sync_claim_until IS NULL THEN TRUE
    WHEN quality_health_sync_claim_until !~ '` + modelQualityHealthSyncCanonicalTimestampRegex + `' THEN TRUE
    ELSE quality_health_sync_claim_until <= db_clock.now_text
  END
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
WHERE ctid = $4::tid
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
