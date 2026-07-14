-- name: ManagementClientIPStatsRangeReady :one
WITH range_state AS (
  SELECT last_success_at
  FROM juhe_stats.stats_job_state
  WHERE scope_type = 'client_ip_range_window'
    AND scope_id = sqlc.arg(start_date)::text || ':' || sqlc.arg(end_date)::text
    AND job_name = 'client_ip_range_window_refresh'
  LIMIT 1
)
SELECT CASE
  WHEN EXISTS (
    SELECT 1
    FROM juhe_stats.client_ip_range_window_dirty_ips
    LIMIT 1
  ) OR EXISTS (
    SELECT 1
    FROM juhe_stats.client_ip_account_range_window_dirty_ips
    LIMIT 1
  ) THEN false
  WHEN EXISTS (SELECT 1 FROM range_state) THEN COALESCE(
    (SELECT last_success_at IS NOT NULL AND last_success_at <> '' FROM range_state),
    false
  )
  ELSE EXISTS (
    SELECT 1
    FROM juhe_stats.client_ip_usage_range_windows
    WHERE start_date = sqlc.arg(start_date)::text
      AND end_date = sqlc.arg(end_date)::text
    LIMIT 1
  )
END::boolean AS range_ready;

-- name: ListManagementClientIPStats :many
WITH client_ip_rows AS (
  SELECT
    registry.ip_hash,
    registry.aggregate_ip_key,
    registry.last_seen_at AS registry_last_seen_at,
    range_stats.request_count,
    range_stats.success_count,
    range_stats.error_count,
    CASE
      WHEN range_stats.request_count > 0
        THEN range_stats.error_count::double precision / range_stats.request_count
      ELSE 0::double precision
    END AS error_rate,
    range_stats.input_tokens,
    range_stats.output_tokens,
    (range_stats.input_tokens + range_stats.output_tokens)::bigint AS total_tokens,
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
    range_stats.last_error_at,
    EXISTS (
      SELECT 1
      FROM juhe_stats.client_ip_policies AS active_policies
      WHERE active_policies.status = 'active'
        AND active_policies.policy_type = 'blacklist'
        AND active_policies.ip_hash = registry.ip_hash
        AND (
          active_policies.expires_at IS NULL
          OR active_policies.expires_at > sqlc.arg(policy_now)::text
        )
      LIMIT 1
    ) AS blacklisted,
    EXISTS (
      SELECT 1
      FROM juhe_stats.client_ip_policies AS active_policies
      WHERE active_policies.status = 'active'
        AND active_policies.policy_type = 'allowlist'
        AND active_policies.ip_hash = registry.ip_hash
        AND (
          active_policies.expires_at IS NULL
          OR active_policies.expires_at > sqlc.arg(policy_now)::text
        )
      LIMIT 1
    ) AS allowlisted
  FROM juhe_stats.client_ip_usage_range_windows AS range_stats
  INNER JOIN juhe_stats.client_ip_registry AS registry
    ON registry.ip_hash = range_stats.ip_hash
  WHERE range_stats.start_date = sqlc.arg(start_date)::text
    AND range_stats.end_date = sqlc.arg(end_date)::text
    AND (
      NOT sqlc.arg(has_last_used_range)::boolean
      OR (
        registry.last_seen_at >= sqlc.arg(last_used_start_at)::text
        AND registry.last_seen_at < sqlc.arg(last_used_end_exclusive_at)::text
      )
    )
    AND (
      sqlc.arg(keyword)::text = ''
      OR (
        (
          registry.aggregate_ip_key COLLATE "C" >= sqlc.arg(keyword)::text
          AND registry.aggregate_ip_key COLLATE "C" < sqlc.arg(keyword_upper)::text
          AND starts_with(registry.aggregate_ip_key, sqlc.arg(keyword)::text)
        )
        OR (
          registry.client_ip COLLATE "C" >= sqlc.arg(keyword)::text
          AND registry.client_ip COLLATE "C" < sqlc.arg(keyword_upper)::text
          AND starts_with(registry.client_ip, sqlc.arg(keyword)::text)
        )
      )
    )
)
SELECT
  ip_hash,
  aggregate_ip_key,
  registry_last_seen_at,
  request_count,
  success_count,
  error_count,
  error_rate,
  input_tokens,
  output_tokens,
  total_tokens,
  cache_read_tokens,
  cache_read_cost_usd,
  cache_write_tokens,
  cache_write_1h_tokens,
  cache_write_cost_usd,
  thinking_tokens,
  input_image_tokens,
  output_image_tokens,
  total_cost_usd,
  duration_ms_sum,
  duration_ms_count,
  duration_ms_max,
  average_duration_ms,
  first_token_ms_sum,
  first_token_ms_count,
  average_first_token_ms,
  active_days,
  last_used_at,
  last_error_at,
  blacklisted,
  allowlisted
FROM client_ip_rows
WHERE sqlc.arg(status_filter)::text = 'all'
  OR (sqlc.arg(status_filter)::text = 'blacklisted' AND blacklisted)
  OR (sqlc.arg(status_filter)::text = 'allowlisted' AND allowlisted)
  OR (
    sqlc.arg(status_filter)::text = 'normal'
    AND NOT blacklisted
    AND NOT allowlisted
  )
ORDER BY
  CASE WHEN sqlc.arg(sort_field)::text = 'requestCount' AND sqlc.arg(sort_order)::text = 'asc' THEN request_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'requestCount' AND sqlc.arg(sort_order)::text = 'desc' THEN request_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'successCount' AND sqlc.arg(sort_order)::text = 'asc' THEN success_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'successCount' AND sqlc.arg(sort_order)::text = 'desc' THEN success_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorCount' AND sqlc.arg(sort_order)::text = 'asc' THEN error_count END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorCount' AND sqlc.arg(sort_order)::text = 'desc' THEN error_count END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorRate' AND sqlc.arg(sort_order)::text = 'asc' THEN error_rate END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'errorRate' AND sqlc.arg(sort_order)::text = 'desc' THEN error_rate END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalTokens' AND sqlc.arg(sort_order)::text = 'asc' THEN total_tokens END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalTokens' AND sqlc.arg(sort_order)::text = 'desc' THEN total_tokens END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalCost' AND sqlc.arg(sort_order)::text = 'asc' THEN total_cost_usd END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'totalCost' AND sqlc.arg(sort_order)::text = 'desc' THEN total_cost_usd END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'activeDays' AND sqlc.arg(sort_order)::text = 'asc' THEN active_days END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'activeDays' AND sqlc.arg(sort_order)::text = 'desc' THEN active_days END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'asc' THEN registry_last_seen_at END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'desc' THEN registry_last_seen_at END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'asc' THEN ip_hash END DESC,
  ip_hash ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementClientIPStatsRequestCountDesc :many
WITH client_ip_rows AS (
  SELECT
    registry.ip_hash,
    registry.aggregate_ip_key,
    registry.last_seen_at AS registry_last_seen_at,
    range_stats.request_count,
    range_stats.success_count,
    range_stats.error_count,
    CASE
      WHEN range_stats.request_count > 0
        THEN range_stats.error_count::double precision / range_stats.request_count
      ELSE 0::double precision
    END AS error_rate,
    range_stats.input_tokens,
    range_stats.output_tokens,
    (range_stats.input_tokens + range_stats.output_tokens)::bigint AS total_tokens,
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
    range_stats.last_error_at,
    EXISTS (
      SELECT 1
      FROM juhe_stats.client_ip_policies AS active_policies
      WHERE active_policies.status = 'active'
        AND active_policies.policy_type = 'blacklist'
        AND active_policies.ip_hash = registry.ip_hash
        AND (
          active_policies.expires_at IS NULL
          OR active_policies.expires_at > sqlc.arg(policy_now)::text
        )
      LIMIT 1
    ) AS blacklisted,
    EXISTS (
      SELECT 1
      FROM juhe_stats.client_ip_policies AS active_policies
      WHERE active_policies.status = 'active'
        AND active_policies.policy_type = 'allowlist'
        AND active_policies.ip_hash = registry.ip_hash
        AND (
          active_policies.expires_at IS NULL
          OR active_policies.expires_at > sqlc.arg(policy_now)::text
        )
      LIMIT 1
    ) AS allowlisted
  FROM juhe_stats.client_ip_usage_range_windows AS range_stats
  INNER JOIN juhe_stats.client_ip_registry AS registry
    ON registry.ip_hash = range_stats.ip_hash
  WHERE range_stats.start_date = sqlc.arg(start_date)::text
    AND range_stats.end_date = sqlc.arg(end_date)::text
    AND (
      NOT sqlc.arg(has_last_used_range)::boolean
      OR (
        registry.last_seen_at >= sqlc.arg(last_used_start_at)::text
        AND registry.last_seen_at < sqlc.arg(last_used_end_exclusive_at)::text
      )
    )
    AND (
      sqlc.arg(keyword)::text = ''
      OR (
        (
          registry.aggregate_ip_key COLLATE "C" >= sqlc.arg(keyword)::text
          AND registry.aggregate_ip_key COLLATE "C" < sqlc.arg(keyword_upper)::text
          AND starts_with(registry.aggregate_ip_key, sqlc.arg(keyword)::text)
        )
        OR (
          registry.client_ip COLLATE "C" >= sqlc.arg(keyword)::text
          AND registry.client_ip COLLATE "C" < sqlc.arg(keyword_upper)::text
          AND starts_with(registry.client_ip, sqlc.arg(keyword)::text)
        )
      )
    )
)
SELECT
  ip_hash,
  aggregate_ip_key,
  registry_last_seen_at,
  request_count,
  success_count,
  error_count,
  error_rate,
  input_tokens,
  output_tokens,
  total_tokens,
  cache_read_tokens,
  cache_read_cost_usd,
  cache_write_tokens,
  cache_write_1h_tokens,
  cache_write_cost_usd,
  thinking_tokens,
  input_image_tokens,
  output_image_tokens,
  total_cost_usd,
  duration_ms_sum,
  duration_ms_count,
  duration_ms_max,
  average_duration_ms,
  first_token_ms_sum,
  first_token_ms_count,
  average_first_token_ms,
  active_days,
  last_used_at,
  last_error_at,
  blacklisted,
  allowlisted
FROM client_ip_rows
WHERE sqlc.arg(status_filter)::text = 'all'
  OR (sqlc.arg(status_filter)::text = 'blacklisted' AND blacklisted)
  OR (sqlc.arg(status_filter)::text = 'allowlisted' AND allowlisted)
  OR (
    sqlc.arg(status_filter)::text = 'normal'
    AND NOT blacklisted
    AND NOT allowlisted
  )
ORDER BY request_count DESC, ip_hash ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;
