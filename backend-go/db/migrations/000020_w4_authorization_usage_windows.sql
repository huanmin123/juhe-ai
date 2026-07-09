-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_summary_daily (
  system_account_id text NOT NULL,
  stat_date text NOT NULL,
  team_filter_id text NOT NULL DEFAULT '',
  resource_filter_type text NOT NULL DEFAULT 'all',
  resource_filter_id text NOT NULL DEFAULT '',
  row_count bigint NOT NULL DEFAULT 0,
  request_count bigint NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd numeric NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd numeric NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd numeric NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_count bigint NOT NULL DEFAULT 0,
  duration_ms_max bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_max bigint NOT NULL DEFAULT 0,
  last_used_at timestamptz,
  last_error_at timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_range_windows (
  system_account_id text NOT NULL,
  start_date text NOT NULL,
  end_date text NOT NULL,
  team_filter_id text NOT NULL DEFAULT '',
  resource_filter_type text NOT NULL DEFAULT 'all',
  resource_filter_id text NOT NULL DEFAULT '',
  request_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd numeric NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd numeric NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd numeric NOT NULL DEFAULT 0,
  last_used_at timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_summary_daily (
  system_account_id text NOT NULL,
  stat_date text NOT NULL,
  team_filter_id text NOT NULL DEFAULT '',
  grantee_filter_system_account_id text NOT NULL DEFAULT '',
  resource_filter_type text NOT NULL DEFAULT 'all',
  resource_filter_id text NOT NULL DEFAULT '',
  row_count bigint NOT NULL DEFAULT 0,
  request_count bigint NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd numeric NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd numeric NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd numeric NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_count bigint NOT NULL DEFAULT 0,
  duration_ms_max bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_max bigint NOT NULL DEFAULT 0,
  last_used_at timestamptz,
  last_error_at timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_range_windows (
  system_account_id text NOT NULL,
  start_date text NOT NULL,
  end_date text NOT NULL,
  team_filter_id text NOT NULL DEFAULT '',
  grantee_filter_system_account_id text NOT NULL DEFAULT '',
  resource_filter_type text NOT NULL DEFAULT 'all',
  resource_filter_id text NOT NULL DEFAULT '',
  request_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd numeric NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd numeric NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd numeric NOT NULL DEFAULT 0,
  last_used_at timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
);

CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_lookup
  ON juhe_stats.authorization_team_usage_summary_daily(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_summary_daily_updated
  ON juhe_stats.authorization_team_usage_summary_daily(updated_at);
CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_lookup
  ON juhe_stats.authorization_team_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_sort
  ON juhe_stats.authorization_team_usage_range_windows(system_account_id, start_date, end_date, total_cost_usd DESC, request_count DESC, last_used_at DESC, team_filter_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_team_usage_range_end
  ON juhe_stats.authorization_team_usage_range_windows(end_date);
CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_lookup
  ON juhe_stats.authorization_user_usage_summary_daily(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_summary_daily_updated
  ON juhe_stats.authorization_user_usage_summary_daily(updated_at);
CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_lookup
  ON juhe_stats.authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_sort
  ON juhe_stats.authorization_user_usage_range_windows(system_account_id, start_date, end_date, team_filter_id, total_cost_usd DESC, request_count DESC, last_used_at DESC, grantee_filter_system_account_id, resource_filter_type, resource_filter_id);
CREATE INDEX IF NOT EXISTS idx_authorization_user_usage_range_end
  ON juhe_stats.authorization_user_usage_range_windows(end_date);

-- +goose Down
-- no-op: authorization usage windows are derived runtime stats.
