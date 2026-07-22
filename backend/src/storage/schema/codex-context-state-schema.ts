import type { DatabaseSync } from 'node:sqlite'

export function applyCodexContextStateSchema(database: DatabaseSync): void {
  database.exec(`
    CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      source_response_id TEXT,
      latest_response_id TEXT,
      latest_compact_id TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_responses (
      response_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      previous_response_id TEXT,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_compacts (
      compact_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      source_response_id TEXT,
      summary_digest TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id);
  `)
}
