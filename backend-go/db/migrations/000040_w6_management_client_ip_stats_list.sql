-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_usage_range_windows (
  ip_hash text NOT NULL,
  start_date text NOT NULL,
  end_date text NOT NULL,
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
  average_duration_ms double precision,
  first_token_ms_sum bigint NOT NULL DEFAULT 0,
  first_token_ms_count bigint NOT NULL DEFAULT 0,
  average_first_token_ms double precision,
  active_days integer NOT NULL DEFAULT 0,
  last_used_at text,
  last_error_at text,
  updated_at text NOT NULL,
  PRIMARY KEY (ip_hash, start_date, end_date)
);

CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_range_window_dirty_ips (
  ip_hash text PRIMARY KEY,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_range_window_dirty_ips (
  ip_hash text PRIMARY KEY,
  updated_at text NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_client_ip_range_requests
  ON juhe_stats.client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_range_end
  ON juhe_stats.client_ip_usage_range_windows(end_date);

CREATE INDEX IF NOT EXISTS idx_client_ip_range_dirty_updated
  ON juhe_stats.client_ip_range_window_dirty_ips(updated_at ASC, ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_dirty_updated
  ON juhe_stats.client_ip_account_range_window_dirty_ips(updated_at ASC, ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_policies_ip
  ON juhe_stats.client_ip_policies(ip_hash, status, policy_type, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_aggregate_ip_key_c
  ON juhe_stats.client_ip_registry(aggregate_ip_key COLLATE "C", ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_client_ip_c
  ON juhe_stats.client_ip_registry(client_ip COLLATE "C", ip_hash);

-- +goose Down
-- no-op: client IP range windows and dirty markers are shared worker data.
