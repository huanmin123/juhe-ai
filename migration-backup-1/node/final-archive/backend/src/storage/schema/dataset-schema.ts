import type { DatabaseSync } from 'node:sqlite'


export function applyDatasetSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;

    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS public_api_logs (
          id TEXT PRIMARY KEY,
          trace_id TEXT,
          source_ref_id TEXT,
          source_name TEXT,
          token_id TEXT,
          token_name TEXT,
          token_prefix TEXT,
          is_test_token INTEGER NOT NULL DEFAULT 0,
          method TEXT NOT NULL,
          path TEXT NOT NULL,
          query_string TEXT,
          client_ip TEXT,
          user_agent TEXT,
          status_code INTEGER,
          success INTEGER NOT NULL DEFAULT 0,
          duration_ms INTEGER,
          request_size_bytes INTEGER NOT NULL DEFAULT 0,
          response_size_bytes INTEGER NOT NULL DEFAULT 0,
          request_capture_status TEXT NOT NULL DEFAULT 'empty',
          response_capture_status TEXT NOT NULL DEFAULT 'empty',
          request_data_json TEXT NOT NULL DEFAULT '{}',
          response_data_json TEXT NOT NULL DEFAULT '{}',
          error_code TEXT,
          error_message TEXT,
          started_at TEXT NOT NULL,
          ended_at TEXT NOT NULL,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
          api_key_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
          account_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          related_account_ids_json TEXT NOT NULL DEFAULT '[]',
          authorization_ids_json TEXT NOT NULL DEFAULT '[]',
          team_scope_ids_json TEXT NOT NULL DEFAULT '[]',
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);

  `)
}
