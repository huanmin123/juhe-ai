-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_usage_range_windows (
  ip_hash text NOT NULL,
  account_id text NOT NULL,
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
  PRIMARY KEY (ip_hash, account_id, start_date, end_date)
);

CREATE INDEX IF NOT EXISTS idx_client_ip_account_range_requests
  ON juhe_stats.client_ip_account_usage_range_windows(
    ip_hash,
    start_date,
    end_date,
    request_count DESC,
    account_id
  );

-- +goose Down
-- no-op: client IP account range windows are shared Node worker data.
