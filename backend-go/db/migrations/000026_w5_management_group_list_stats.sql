-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.group_account_stats (
  system_account_id text NOT NULL,
  group_id text NOT NULL,
  total integer NOT NULL DEFAULT 0,
  available integer NOT NULL DEFAULT 0,
  active integer NOT NULL DEFAULT 0,
  disabled integer NOT NULL DEFAULT 0,
  error integer NOT NULL DEFAULT 0,
  rate_limited integer NOT NULL DEFAULT 0,
  current_concurrency integer NOT NULL DEFAULT 0,
  concurrency_limit integer NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, group_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_totals (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
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
  PRIMARY KEY (system_account_id, scope_type, scope_id)
);

CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_daily (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  stat_date text NOT NULL,
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
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
);

CREATE TABLE IF NOT EXISTS juhe_stats.stats_job_state (
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  job_name text NOT NULL,
  cursor_created_at text,
  cursor_id text,
  last_success_at text,
  last_error_message text,
  lag_seconds integer,
  updated_at text NOT NULL,
  PRIMARY KEY (scope_type, scope_id, job_name)
);

CREATE INDEX IF NOT EXISTS idx_group_account_stats_group
  ON juhe_stats.group_account_stats(group_id);

CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor
  ON juhe_stats.stats_job_state(scope_type, job_name, cursor_created_at, cursor_id)
  WHERE cursor_created_at IS NOT NULL
    AND cursor_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_stats_job_state_usage_shard_cursor_floor_any_job
  ON juhe_stats.stats_job_state(scope_type, cursor_created_at, cursor_id, job_name)
  WHERE cursor_created_at IS NOT NULL
    AND cursor_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_updated
  ON juhe_stats.usage_stats_totals(updated_at);

CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date
  ON juhe_stats.usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);

CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date
  ON juhe_stats.usage_stats_daily(stat_date);

CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_updated
  ON juhe_stats.usage_stats_daily(updated_at);

-- +goose Down
-- no-op: management group list stats are maintained by the stats worker.
