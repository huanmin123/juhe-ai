package runtimelog

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS runtime_logs (
  id TEXT PRIMARY KEY,
  log_file TEXT,
  log_offset INTEGER,
  line_number INTEGER,
  time TEXT NOT NULL,
  level TEXT NOT NULL,
  trace_id TEXT,
  event TEXT,
  message TEXT,
  error_message TEXT,
  raw_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_log_file_cursors (
  log_file TEXT PRIMARY KEY,
  file_identity TEXT,
  cursor_offset INTEGER NOT NULL DEFAULT 0,
  line_number INTEGER NOT NULL DEFAULT 0,
  file_size INTEGER NOT NULL DEFAULT 0,
  truncation_generation INTEGER NOT NULL DEFAULT 0,
  file_mtime_ms INTEGER,
  last_read_at TEXT,
  last_error_message TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_log_index_owner_leases (
  lease_key TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  fence_token INTEGER NOT NULL DEFAULT 0,
  lease_until TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_log_facet_summary (
  bucket_key TEXT PRIMARY KEY,
  total_count INTEGER NOT NULL DEFAULT 0,
  earliest_time TEXT,
  latest_time TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_log_level_facets (
  bucket_key TEXT NOT NULL,
  level TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bucket_key, level)
);
CREATE TABLE IF NOT EXISTS runtime_log_event_facets (
  bucket_key TEXT NOT NULL,
  event TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  latest_time TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bucket_key, event)
);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_time ON runtime_logs(time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_id_time ON runtime_logs(trace_id, time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_log_file_cursors_updated ON runtime_log_file_cursors(updated_at);
CREATE INDEX IF NOT EXISTS idx_runtime_log_facet_summary_latest ON runtime_log_facet_summary(latest_time);
CREATE INDEX IF NOT EXISTS idx_runtime_log_event_facets_latest ON runtime_log_event_facets(latest_time DESC, event);
`

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs (
  id text PRIMARY KEY,
  log_file text,
  log_offset bigint,
  line_number integer,
  time text NOT NULL,
  level text NOT NULL,
  trace_id text,
  event text,
  message text,
  error_message text,
  raw_json text NOT NULL,
  created_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_file_cursors (
  log_file text PRIMARY KEY,
  file_identity text,
  cursor_offset bigint NOT NULL DEFAULT 0,
  line_number integer NOT NULL DEFAULT 0,
  file_size bigint NOT NULL DEFAULT 0,
  truncation_generation integer NOT NULL DEFAULT 0,
  file_mtime_ms bigint,
  last_read_at text,
  last_error_message text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_index_owner_leases (
  lease_key text PRIMARY KEY,
  owner_id text NOT NULL,
  fence_token bigint NOT NULL DEFAULT 0,
  lease_until text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (
  bucket_key text PRIMARY KEY,
  total_count integer NOT NULL DEFAULT 0,
  earliest_time text,
  latest_time text,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (
  bucket_key text NOT NULL,
  level text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (bucket_key, level)
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (
  bucket_key text NOT NULL,
  event text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  latest_time text,
  updated_at text NOT NULL,
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
