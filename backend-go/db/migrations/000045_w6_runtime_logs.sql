-- +goose Up
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
  file_mtime_ms bigint,
  last_read_at text,
  last_error_message text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (
  bucket_key text PRIMARY KEY,
  total_count bigint NOT NULL DEFAULT 0,
  earliest_time text,
  latest_time text,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (
  bucket_key text NOT NULL,
  level text NOT NULL,
  count bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (bucket_key, level)
);

CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (
  bucket_key text NOT NULL,
  event text NOT NULL,
  count bigint NOT NULL DEFAULT 0,
  latest_time text,
  updated_at text NOT NULL,
  PRIMARY KEY (bucket_key, event)
);

CREATE INDEX IF NOT EXISTS idx_runtime_logs_time
  ON juhe_dataset.runtime_logs (time DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_c_time
  ON juhe_dataset.runtime_logs ((trace_id COLLATE "C"), time DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_runtime_logs_level_time
  ON juhe_dataset.runtime_logs (level, time DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_runtime_logs_event_time
  ON juhe_dataset.runtime_logs (event, time DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_runtime_log_file_cursors_updated
  ON juhe_dataset.runtime_log_file_cursors (updated_at);

CREATE INDEX IF NOT EXISTS idx_runtime_log_facet_summary_latest
  ON juhe_dataset.runtime_log_facet_summary (latest_time);

CREATE INDEX IF NOT EXISTS idx_runtime_log_event_facets_latest
  ON juhe_dataset.runtime_log_event_facets (latest_time DESC, event);

-- +goose Down
-- no-op: runtime log indexes, writer cursors, and facets are shared Node writer data.
