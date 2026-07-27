-- +goose Up
-- W6 management stats overview reads Node-maintained source and published
-- windows. These definitions complete the fresh-Goose catalog without
-- changing the current stats writer owner.
CREATE TABLE IF NOT EXISTS juhe_stats.usage_model_daily (
  system_account_id text NOT NULL,
  stat_date text NOT NULL,
  provider_code text NOT NULL DEFAULT 'unknown',
  model text NOT NULL DEFAULT 'unknown',
  request_count bigint NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd double precision NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd double precision NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd double precision NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, stat_date, provider_code, model)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_error_daily (
  system_account_id text NOT NULL,
  stat_date text NOT NULL,
  error_group text NOT NULL DEFAULT 'unknown',
  provider_code text NOT NULL DEFAULT 'unknown',
  error_code text NOT NULL DEFAULT 'unknown',
  status_code integer NOT NULL DEFAULT 0,
  error_message text,
  request_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, stat_date, error_group, provider_code, error_code, status_code)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_overview_summary_windows (
  system_account_id text NOT NULL,
  window_key text NOT NULL,
  start_date text NOT NULL DEFAULT '',
  end_date text NOT NULL DEFAULT '',
  request_count bigint NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd double precision NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd double precision NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd double precision NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  last_used_at text,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, window_key)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_overview_trend_windows (
  system_account_id text NOT NULL,
  window_key text NOT NULL,
  start_date text NOT NULL DEFAULT '',
  end_date text NOT NULL DEFAULT '',
  bucket_key text NOT NULL,
  request_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd double precision NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd double precision NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd double precision NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_count bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, window_key, bucket_key)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_model_rank_windows (
  system_account_id text NOT NULL,
  window_key text NOT NULL,
  start_date text NOT NULL DEFAULT '',
  end_date text NOT NULL DEFAULT '',
  rank integer NOT NULL,
  provider_code text NOT NULL DEFAULT 'unknown',
  model text NOT NULL DEFAULT 'unknown',
  request_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd double precision NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd double precision NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd double precision NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, window_key, rank, provider_code, model)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_error_rank_windows (
  system_account_id text NOT NULL,
  window_key text NOT NULL,
  start_date text NOT NULL DEFAULT '',
  end_date text NOT NULL DEFAULT '',
  rank integer NOT NULL,
  provider_code text NOT NULL DEFAULT 'unknown',
  error_code text NOT NULL DEFAULT 'unknown',
  status_code integer NOT NULL DEFAULT 0,
  error_message text,
  error_count bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, window_key, rank, provider_code, error_code, status_code)
);

CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date
  ON juhe_stats.usage_model_daily(system_account_id, stat_date, model);
CREATE INDEX IF NOT EXISTS idx_usage_model_daily_stat_date
  ON juhe_stats.usage_model_daily(stat_date);
CREATE INDEX IF NOT EXISTS idx_usage_model_daily_updated
  ON juhe_stats.usage_model_daily(updated_at);

CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date
  ON juhe_stats.usage_error_daily(system_account_id, stat_date, error_code);
CREATE INDEX IF NOT EXISTS idx_usage_error_daily_stat_date
  ON juhe_stats.usage_error_daily(stat_date);
CREATE INDEX IF NOT EXISTS idx_usage_error_daily_updated
  ON juhe_stats.usage_error_daily(updated_at);

CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_end
  ON juhe_stats.usage_overview_summary_windows(end_date);
CREATE INDEX IF NOT EXISTS idx_usage_overview_summary_windows_prewarm_order
  ON juhe_stats.usage_overview_summary_windows(window_key, request_count DESC, last_used_at DESC, system_account_id)
  WHERE request_count > 0
    AND system_account_id <> 'global';

CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_lookup
  ON juhe_stats.usage_overview_trend_windows(system_account_id, window_key, bucket_key);
CREATE INDEX IF NOT EXISTS idx_usage_overview_trend_windows_end
  ON juhe_stats.usage_overview_trend_windows(end_date);

CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_lookup
  ON juhe_stats.usage_model_rank_windows(system_account_id, window_key, rank);
CREATE INDEX IF NOT EXISTS idx_usage_model_rank_windows_end
  ON juhe_stats.usage_model_rank_windows(end_date);

CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_lookup
  ON juhe_stats.usage_error_rank_windows(system_account_id, window_key, rank);
CREATE INDEX IF NOT EXISTS idx_usage_error_rank_windows_end
  ON juhe_stats.usage_error_rank_windows(end_date);

-- +goose Down
-- no-op: these tables are shared with the current Node stats writer. Dropping
-- them during rollback would destroy production-derived data.
