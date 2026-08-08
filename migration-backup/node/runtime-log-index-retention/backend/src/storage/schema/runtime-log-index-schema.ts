import type { DatabaseSync } from 'node:sqlite'

export function applyRuntimeLogIndexSchema(database: DatabaseSync): void {
  database.exec(`
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
  `)
}
