-- name: ListManagementAPIKeys :many
SELECT
  api_keys.id,
  api_keys.system_account_id,
  system_accounts.display_name AS system_account_name,
  api_keys.name,
  api_keys.description,
  api_keys.key_prefix,
  api_keys.key_suffix,
  api_keys.status,
  api_keys.is_default,
  api_keys.route_strategy_id,
  route_strategies.name AS route_strategy_name,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.status AS route_strategy_status,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  api_keys.availability_schedule_json
FROM juhe_business.api_keys AS api_keys
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = api_keys.system_account_id
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE (
    sqlc.arg(system_account_id)::text = ''
    OR api_keys.system_account_id = sqlc.arg(system_account_id)::text
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      api_keys.name COLLATE "C" >= sqlc.arg(keyword)::text
      AND api_keys.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(api_keys.name, sqlc.arg(keyword)::text)
    )
  )
  AND (
    sqlc.arg(status)::text = ''
    OR api_keys.status = sqlc.arg(status)::text
  )
  AND (
    sqlc.arg(route_strategy_id)::text = ''
    OR api_keys.route_strategy_id = sqlc.arg(route_strategy_id)::text
  )
ORDER BY
  api_keys.is_default DESC,
  api_keys.updated_at DESC,
  api_keys.created_at DESC,
  api_keys.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementAPIKeyUsageTotals :many
SELECT
  usage_stats.scope_id,
  usage_stats.request_count,
  usage_stats.input_tokens,
  usage_stats.output_tokens,
  usage_stats.cache_read_tokens,
  usage_stats.cache_read_cost_usd,
  usage_stats.cache_write_tokens,
  usage_stats.cache_write_1h_tokens,
  usage_stats.cache_write_cost_usd,
  usage_stats.thinking_tokens,
  usage_stats.input_image_tokens,
  usage_stats.output_image_tokens,
  usage_stats.total_cost_usd,
  usage_stats.last_used_at
FROM juhe_stats.usage_stats_totals AS usage_stats
WHERE usage_stats.scope_type = 'api_key'
  AND usage_stats.scope_id = ANY(sqlc.arg(api_key_ids)::text[])
ORDER BY usage_stats.scope_id ASC;
