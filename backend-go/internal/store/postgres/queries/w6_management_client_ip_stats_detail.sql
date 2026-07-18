-- name: GetManagementClientIPStatsRegistry :one
SELECT
  ip_hash,
  aggregate_ip_key,
  last_seen_at
FROM juhe_stats.client_ip_registry
WHERE ip_hash = sqlc.arg(ip_hash)::text
LIMIT 1;

-- name: ListManagementClientIPAccountUsage :many
SELECT
  range_stats.account_id,
  accounts.name AS account_name,
  accounts.system_account_id AS account_owner_system_account_id,
  system_accounts.display_name AS account_owner_system_account_name,
  range_stats.request_count,
  range_stats.success_count,
  range_stats.error_count,
  range_stats.input_tokens,
  range_stats.output_tokens,
  range_stats.cache_read_tokens,
  range_stats.cache_read_cost_usd,
  range_stats.cache_write_tokens,
  range_stats.cache_write_1h_tokens,
  range_stats.cache_write_cost_usd,
  range_stats.thinking_tokens,
  range_stats.input_image_tokens,
  range_stats.output_image_tokens,
  range_stats.total_cost_usd,
  range_stats.duration_ms_sum,
  range_stats.duration_ms_count,
  range_stats.duration_ms_max,
  range_stats.average_duration_ms,
  range_stats.first_token_ms_sum,
  range_stats.first_token_ms_count,
  range_stats.average_first_token_ms,
  range_stats.active_days,
  range_stats.last_used_at,
  range_stats.last_error_at
FROM juhe_stats.client_ip_account_usage_range_windows AS range_stats
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = range_stats.account_id
LEFT JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = accounts.system_account_id
WHERE range_stats.ip_hash = sqlc.arg(ip_hash)::text
  AND range_stats.start_date = sqlc.arg(start_date)::text
  AND range_stats.end_date = sqlc.arg(end_date)::text
ORDER BY
  CASE WHEN sqlc.arg(sort_field)::text = 'requestCount' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.request_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'requestCount' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.request_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'successCount' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.success_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'successCount' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.success_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorCount' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.error_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorCount' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.error_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorRate' AND sqlc.arg(sort_order)::text = 'asc' THEN CASE WHEN range_stats.request_count > 0 THEN range_stats.error_count::real / range_stats.request_count ELSE 0::real END END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorRate' AND sqlc.arg(sort_order)::text = 'desc' THEN CASE WHEN range_stats.request_count > 0 THEN range_stats.error_count::real / range_stats.request_count ELSE 0::real END END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalTokens' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.input_tokens + range_stats.output_tokens END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalTokens' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.input_tokens + range_stats.output_tokens END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalCost' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.total_cost_usd END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalCost' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.total_cost_usd END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'activeDays' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.active_days END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'activeDays' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.active_days END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'asc' THEN range_stats.last_used_at END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'desc' THEN range_stats.last_used_at END DESC,
  CASE WHEN sqlc.arg(sort_order)::text = 'asc' THEN range_stats.account_id END DESC,
  range_stats.account_id ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementClientIPAccountUsageRequestCountDesc :many
SELECT
  range_stats.account_id,
  accounts.name AS account_name,
  accounts.system_account_id AS account_owner_system_account_id,
  system_accounts.display_name AS account_owner_system_account_name,
  range_stats.request_count,
  range_stats.success_count,
  range_stats.error_count,
  range_stats.input_tokens,
  range_stats.output_tokens,
  range_stats.cache_read_tokens,
  range_stats.cache_read_cost_usd,
  range_stats.cache_write_tokens,
  range_stats.cache_write_1h_tokens,
  range_stats.cache_write_cost_usd,
  range_stats.thinking_tokens,
  range_stats.input_image_tokens,
  range_stats.output_image_tokens,
  range_stats.total_cost_usd,
  range_stats.duration_ms_sum,
  range_stats.duration_ms_count,
  range_stats.duration_ms_max,
  range_stats.average_duration_ms,
  range_stats.first_token_ms_sum,
  range_stats.first_token_ms_count,
  range_stats.average_first_token_ms,
  range_stats.active_days,
  range_stats.last_used_at,
  range_stats.last_error_at
FROM juhe_stats.client_ip_account_usage_range_windows AS range_stats
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = range_stats.account_id
LEFT JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = accounts.system_account_id
WHERE range_stats.ip_hash = sqlc.arg(ip_hash)::text
  AND range_stats.start_date = sqlc.arg(start_date)::text
  AND range_stats.end_date = sqlc.arg(end_date)::text
ORDER BY range_stats.request_count DESC, range_stats.account_id ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;
