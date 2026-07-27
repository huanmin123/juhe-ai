-- +goose Up
-- W6 progressive stats reads depend on Node-maintained derived data.  These
-- definitions make a fresh Goose database compatible without changing the
-- current writer owner.
CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_hourly (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  stat_hour text NOT NULL,
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
  duration_ms_max bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_max bigint NOT NULL DEFAULT 0,
  last_used_at text,
  last_error_at text,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_rank_snapshots (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  window_key text NOT NULL,
  metric text NOT NULL,
  snapshot_at text NOT NULL,
  rank integer NOT NULL,
  scope_id text NOT NULL,
  metric_value double precision NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.ai_performance_summary_windows (
  system_account_id text NOT NULL,
  window_key text NOT NULL,
  start_date text NOT NULL DEFAULT '',
  end_date text NOT NULL DEFAULT '',
  request_count bigint NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_count bigint NOT NULL DEFAULT 0,
  duration_ms_max bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_max bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, window_key)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_scope_range_windows (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  start_date text NOT NULL,
  end_date text NOT NULL,
  window_key text GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
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
  duration_ms_max bigint NOT NULL DEFAULT 0,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  first_token_ms_max bigint NOT NULL DEFAULT 0,
  active_days integer NOT NULL DEFAULT 0,
  last_used_at text,
  last_error_at text,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
);

CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour
  ON juhe_stats.usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour);
CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_stat_hour
  ON juhe_stats.usage_stats_hourly(system_account_id, scope_type, stat_hour, scope_id);
CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_hour
  ON juhe_stats.usage_stats_hourly(stat_hour);
CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_updated
  ON juhe_stats.usage_stats_hourly(updated_at);

CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_lookup
  ON juhe_stats.usage_rank_snapshots(system_account_id, scope_type, window_key, metric, snapshot_at DESC, rank);
CREATE INDEX IF NOT EXISTS idx_usage_rank_snapshots_snapshot
  ON juhe_stats.usage_rank_snapshots(snapshot_at);

CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_lookup
  ON juhe_stats.ai_performance_summary_windows(system_account_id, window_key);
CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_windows_end
  ON juhe_stats.ai_performance_summary_windows(end_date);

CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_lookup
  ON juhe_stats.usage_scope_range_windows(system_account_id, scope_type, scope_id, window_key);
CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_range_lookup
  ON juhe_stats.usage_scope_range_windows(system_account_id, scope_type, window_key, scope_id);
CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_account_usage_order
  ON juhe_stats.usage_scope_range_windows(system_account_id, scope_type, window_key, request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id);
CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end
  ON juhe_stats.usage_scope_range_windows(end_date);
CREATE INDEX IF NOT EXISTS idx_usage_scope_range_windows_end_start
  ON juhe_stats.usage_scope_range_windows(end_date, start_date);

-- +goose Down
-- no-op: these tables are shared with the current Node stats writer.  Dropping
-- them during rollback would destroy production-derived data.
