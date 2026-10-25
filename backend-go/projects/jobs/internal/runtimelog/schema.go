package runtimelog

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs (
  id text PRIMARY KEY,
  log_file text,
  log_offset bigint,
  line_number integer,
  time timestamptz NOT NULL,
  level text NOT NULL,
  trace_id text,
  event text,
  message text,
  error_message text,
  raw_json text NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_file_cursors (
  log_file text PRIMARY KEY,
  file_identity text,
  cursor_offset bigint NOT NULL DEFAULT 0,
  line_number integer NOT NULL DEFAULT 0,
  file_size bigint NOT NULL DEFAULT 0,
  truncation_generation integer NOT NULL DEFAULT 0,
  file_mtime_ms bigint,
  last_read_at timestamptz,
  last_error_message text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_index_owner_leases (
  lease_key text PRIMARY KEY,
  owner_id text NOT NULL,
  fence_token bigint NOT NULL DEFAULT 0,
  lease_until timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (
  bucket_key text PRIMARY KEY,
  total_count integer NOT NULL DEFAULT 0,
  earliest_time timestamptz,
  latest_time timestamptz,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (
  bucket_key text NOT NULL,
  level text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (bucket_key, level)
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (
  bucket_key text NOT NULL,
  event text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  latest_time timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (bucket_key, event)
);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_time ON juhe_dataset.runtime_logs(time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_id_time ON juhe_dataset.runtime_logs(trace_id, time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_c_time ON juhe_dataset.runtime_logs((trace_id COLLATE "C"), time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_log_file_cursors_updated ON juhe_dataset.runtime_log_file_cursors(updated_at);
CREATE INDEX IF NOT EXISTS idx_runtime_log_facet_summary_latest ON juhe_dataset.runtime_log_facet_summary(latest_time);
CREATE INDEX IF NOT EXISTS idx_runtime_log_event_facets_latest ON juhe_dataset.runtime_log_event_facets(latest_time DESC, event);
ALTER TABLE juhe_dataset.runtime_log_index_owner_leases ADD COLUMN IF NOT EXISTS fence_token bigint NOT NULL DEFAULT 0;
`
