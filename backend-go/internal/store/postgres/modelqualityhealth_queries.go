package postgres

const upsertModelQualityHealthFailureSQL = `
INSERT INTO juhe_stats.account_quality_health_hourly AS health_row (
  account_id, system_account_id, provider_code, stat_hour, observed_at,
  model_check_run_id, model, profile, score, threshold, level,
  error_code, error_message, updated_at
) VALUES (
  $1, $2, $3, $4, $5,
  $6, $7, $8, $9, $10, $11,
  $12, $13, $14
)
ON CONFLICT (account_id, stat_hour) DO UPDATE SET
  system_account_id = EXCLUDED.system_account_id,
  provider_code = EXCLUDED.provider_code,
  observed_at = EXCLUDED.observed_at,
  model_check_run_id = EXCLUDED.model_check_run_id,
  model = EXCLUDED.model,
  profile = EXCLUDED.profile,
  score = EXCLUDED.score,
  threshold = EXCLUDED.threshold,
  level = EXCLUDED.level,
  error_code = EXCLUDED.error_code,
  error_message = EXCLUDED.error_message,
  updated_at = EXCLUDED.updated_at
WHERE EXCLUDED.observed_at > health_row.observed_at
   OR (EXCLUDED.observed_at = health_row.observed_at
       AND EXCLUDED.model_check_run_id > health_row.model_check_run_id)
`
