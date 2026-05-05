import type { DatabaseSync } from 'node:sqlite'

import { hashPassword } from './crypto.js'
import { DEFAULT_GLOBAL_SETTINGS, DEFAULT_OPENAI_GROUP, DEFAULT_SYSTEM_SETTINGS, OPENAI_PROVIDER_SEED } from './schema-defaults.js'
import { ensureSchemaMaintenance } from './schema-maintenance.js'

export function applySchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;
    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS system_accounts (
      id TEXT PRIMARY KEY,
      username TEXT NOT NULL UNIQUE,
      display_name TEXT NOT NULL,
      description TEXT,
      role TEXT NOT NULL DEFAULT 'user',
      status TEXT NOT NULL DEFAULT 'active',
      password_hash TEXT NOT NULL,
      must_change_password INTEGER NOT NULL DEFAULT 0,
      last_login_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      token_hash TEXT NOT NULL UNIQUE,
      expires_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      last_seen_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS global_settings (
      key TEXT PRIMARY KEY,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS providers (
      id TEXT PRIMARY KEY,
      code TEXT NOT NULL UNIQUE,
      name TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      base_url TEXT NOT NULL,
      account_types_json TEXT NOT NULL,
      capabilities_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS proxy_profiles (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      description TEXT,
      type TEXT NOT NULL,
      host TEXT NOT NULL,
      port INTEGER NOT NULL,
      username TEXT,
      password_encrypted TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      test_status TEXT NOT NULL DEFAULT 'unknown',
      last_tested_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS error_policies (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      rules_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      provider_code TEXT NOT NULL,
      name TEXT NOT NULL,
      type TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      credentials_encrypted TEXT NOT NULL,
      credential_fingerprint TEXT,
      credential_mask TEXT NOT NULL DEFAULT '',
      proxy_profile_id TEXT,
      concurrency_limit INTEGER NOT NULL DEFAULT 20,
      passthrough_enabled INTEGER NOT NULL DEFAULT 1,
      error_policy_id TEXT,
      priority INTEGER NOT NULL DEFAULT 0,
      schedulable INTEGER NOT NULL DEFAULT 1,
      notes TEXT,
      account_expires_at TEXT,
      last_used_at TEXT,
      cooldown_until TEXT,
      last_error_message TEXT,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (proxy_profile_id) REFERENCES proxy_profiles(id),
      FOREIGN KEY (error_policy_id) REFERENCES error_policies(id)
    );

    CREATE TABLE IF NOT EXISTS system_teams (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      description TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_team_members (
      id TEXT PRIMARY KEY,
      team_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      member_role TEXT NOT NULL DEFAULT 'member',
      status TEXT NOT NULL DEFAULT 'active',
      joined_at TEXT NOT NULL,
      removed_at TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (team_id) REFERENCES system_teams(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      effective_source_type TEXT,
      effective_source_team_id TEXT,
      activated_at TEXT,
      last_source_changed_at TEXT,
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      model_policy_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      revoked_reason TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorization_sources (
      id TEXT PRIMARY KEY,
      authorization_id TEXT NOT NULL,
      source_type TEXT NOT NULL,
      source_team_id TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      activated_at TEXT,
      ended_at TEXT,
      ended_reason TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (source_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );


    CREATE TABLE IF NOT EXISTS team_resource_authorization_grants (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      team_id TEXT NOT NULL,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      model_policy_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      is_default INTEGER NOT NULL DEFAULT 0,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS group_accounts (
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      weight INTEGER NOT NULL DEFAULT 1,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (group_id, account_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS group_account_stats (
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      total INTEGER NOT NULL DEFAULT 0,
      available INTEGER NOT NULL DEFAULT 0,
      active INTEGER NOT NULL DEFAULT 0,
      disabled INTEGER NOT NULL DEFAULT 0,
      error INTEGER NOT NULL DEFAULT 0,
      rate_limited INTEGER NOT NULL DEFAULT 0,
      current_concurrency INTEGER NOT NULL DEFAULT 0,
      concurrency_limit INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, group_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      description TEXT,
      key_hash TEXT NOT NULL UNIQUE,
      key_prefix TEXT NOT NULL,
      key_secret_encrypted TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      group_id TEXT NOT NULL,
      group_authorization_id TEXT,
      expires_at TEXT,
      rate_limit INTEGER,
      quota_limit INTEGER,
      scopes_json TEXT NOT NULL DEFAULT '[]',
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (group_id) REFERENCES groups(id),
      FOREIGN KEY (group_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      trace_id TEXT NOT NULL,
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      endpoint TEXT,
      provider_code TEXT,
      model TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      first_token_ms INTEGER,
      duration_ms INTEGER,
      input_tokens INTEGER,
      output_tokens INTEGER,
      cache_read_tokens INTEGER,
      cost_usd REAL,
      error_code TEXT,
      error_message TEXT,
      request_snapshot_json TEXT,
      response_snapshot_json TEXT,
      account_owner_system_account_id TEXT,
      group_owner_system_account_id TEXT,
      account_access_type TEXT,
      group_access_type TEXT,
      account_authorization_id TEXT,
      group_authorization_id TEXT,
      created_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS audit_logs (
      id TEXT PRIMARY KEY,
      trace_id TEXT NOT NULL,
      system_account_id TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      provider_code TEXT,
      method TEXT NOT NULL,
      path TEXT NOT NULL,
      query_string TEXT,
      model TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      client_ip TEXT,
      user_agent TEXT,
      audit_outcome TEXT NOT NULL,
      success INTEGER NOT NULL DEFAULT 0,
      final_status_code INTEGER,
      error_phase TEXT,
      error_code TEXT,
      error_message TEXT,
      sample_bucket INTEGER NOT NULL,
      sample_reason TEXT NOT NULL,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      payload_count INTEGER NOT NULL DEFAULT 0,
      payload_bytes INTEGER NOT NULL DEFAULT 0,
      capture_status TEXT NOT NULL DEFAULT 'complete',
      started_at TEXT NOT NULL,
      ended_at TEXT NOT NULL,
      duration_ms INTEGER,
      first_token_ms INTEGER,
      created_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS audit_log_attempts (
      id TEXT PRIMARY KEY,
      audit_log_id TEXT NOT NULL,
      attempt_index INTEGER NOT NULL,
      account_id TEXT,
      account_owner_system_account_id TEXT,
      group_id TEXT,
      proxy_url TEXT,
      provider_code TEXT,
      upstream_method TEXT NOT NULL,
      upstream_url TEXT NOT NULL,
      upstream_status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      error_phase TEXT,
      error_code TEXT,
      error_message TEXT,
      started_at TEXT NOT NULL,
      ended_at TEXT,
      duration_ms INTEGER,
      FOREIGN KEY (audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS audit_log_payloads (
      id TEXT PRIMARY KEY,
      audit_log_id TEXT NOT NULL,
      attempt_id TEXT,
      part_type TEXT NOT NULL,
      sequence_index INTEGER NOT NULL DEFAULT 0,
      content_type TEXT,
      content_encoding TEXT,
      headers_encrypted TEXT,
      body_encrypted TEXT,
      body_sha256 TEXT,
      size_bytes INTEGER NOT NULL DEFAULT 0,
      created_at TEXT NOT NULL,
      FOREIGN KEY (audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE,
      FOREIGN KEY (attempt_id) REFERENCES audit_log_attempts(id) ON DELETE SET NULL
    );

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

    CREATE VIRTUAL TABLE IF NOT EXISTS runtime_log_search USING fts5(
      log_id UNINDEXED,
      trace_id,
      event,
      message,
      error_message,
      raw_json,
      tokenize = 'trigram'
    );

    CREATE TABLE IF NOT EXISTS account_usage_snapshots (
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      account_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      source TEXT,
      snapshot_json TEXT NOT NULL,
      refresh_status TEXT,
      last_attempt_at TEXT,
      last_success_at TEXT,
      next_refresh_after TEXT,
      last_error_message TEXT,
      updated_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, account_id, kind),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS usage_stats_totals (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      duration_ms_sum INTEGER NOT NULL DEFAULT 0,
      duration_ms_count INTEGER NOT NULL DEFAULT 0,
      first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
      first_token_ms_count INTEGER NOT NULL DEFAULT 0,
      last_used_at TEXT,
      last_error_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    );

    CREATE TABLE IF NOT EXISTS usage_stats_daily (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      stat_date TEXT NOT NULL,
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      duration_ms_sum INTEGER NOT NULL DEFAULT 0,
      duration_ms_count INTEGER NOT NULL DEFAULT 0,
      first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
      first_token_ms_count INTEGER NOT NULL DEFAULT 0,
      last_used_at TEXT,
      last_error_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
    );

    CREATE TABLE IF NOT EXISTS usage_stats_hourly (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      stat_hour TEXT NOT NULL,
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      duration_ms_sum INTEGER NOT NULL DEFAULT 0,
      duration_ms_count INTEGER NOT NULL DEFAULT 0,
      first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
      first_token_ms_count INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
    );

    CREATE TABLE IF NOT EXISTS usage_model_daily (
      system_account_id TEXT NOT NULL,
      stat_date TEXT NOT NULL,
      provider_code TEXT NOT NULL DEFAULT 'unknown',
      model TEXT NOT NULL DEFAULT 'unknown',
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, stat_date, provider_code, model)
    );

    CREATE TABLE IF NOT EXISTS usage_model_hourly (
      system_account_id TEXT NOT NULL,
      stat_hour TEXT NOT NULL,
      provider_code TEXT NOT NULL DEFAULT 'unknown',
      model TEXT NOT NULL DEFAULT 'unknown',
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, stat_hour, provider_code, model)
    );

    CREATE TABLE IF NOT EXISTS usage_error_daily (
      system_account_id TEXT NOT NULL,
      stat_date TEXT NOT NULL,
      error_group TEXT NOT NULL DEFAULT 'unknown',
      provider_code TEXT NOT NULL DEFAULT 'unknown',
      error_code TEXT NOT NULL DEFAULT 'unknown',
      status_code INTEGER NOT NULL DEFAULT 0,
      error_message TEXT,
      request_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, stat_date, error_group, error_code)
    );

    CREATE TABLE IF NOT EXISTS usage_error_hourly (
      system_account_id TEXT NOT NULL,
      stat_hour TEXT NOT NULL,
      error_group TEXT NOT NULL DEFAULT 'unknown',
      provider_code TEXT NOT NULL DEFAULT 'unknown',
      error_code TEXT NOT NULL DEFAULT 'unknown',
      status_code INTEGER NOT NULL DEFAULT 0,
      error_message TEXT,
      request_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, stat_hour, error_group, error_code)
    );

    CREATE TABLE IF NOT EXISTS stats_job_state (
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      job_name TEXT NOT NULL,
      cursor_created_at TEXT,
      cursor_id TEXT,
      last_success_at TEXT,
      last_error_message TEXT,
      lag_seconds INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (scope_type, scope_id, job_name)
    );

    CREATE TABLE IF NOT EXISTS system_metrics_samples (
      id TEXT PRIMARY KEY,
      sampled_at TEXT NOT NULL,
      cpu_percent REAL,
      memory_used_percent REAL,
      memory_total_bytes INTEGER,
      memory_free_bytes INTEGER,
      process_rss_bytes INTEGER,
      process_heap_used_bytes INTEGER,
      process_heap_total_bytes INTEGER,
      event_loop_lag_ms REAL,
      network_rx_bytes_per_sec REAL,
      network_tx_bytes_per_sec REAL,
      network_rx_total_bytes INTEGER,
      network_tx_total_bytes INTEGER,
      db_file_bytes INTEGER,
      stats_lag_seconds INTEGER
    );

    CREATE TABLE IF NOT EXISTS system_metrics_hourly (
      stat_hour TEXT PRIMARY KEY,
      sample_count INTEGER NOT NULL DEFAULT 0,
      cpu_percent_sum REAL NOT NULL DEFAULT 0,
      cpu_percent_max REAL,
      memory_used_percent_sum REAL NOT NULL DEFAULT 0,
      memory_used_percent_max REAL,
      process_rss_bytes_sum INTEGER NOT NULL DEFAULT 0,
      process_rss_bytes_max INTEGER,
      process_heap_used_bytes_sum INTEGER NOT NULL DEFAULT 0,
      process_heap_used_bytes_max INTEGER,
      event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
      event_loop_lag_ms_max REAL,
      network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
      network_rx_bytes_per_sec_max REAL,
      network_rx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
      network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
      network_tx_bytes_per_sec_max REAL,
      network_tx_bytes_per_sec_count INTEGER NOT NULL DEFAULT 0,
      network_rx_total_bytes_max INTEGER,
      network_tx_total_bytes_max INTEGER,
      db_file_bytes_max INTEGER,
      stats_lag_seconds_max INTEGER,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_settings (
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      key TEXT NOT NULL,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, key),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status);
    CREATE INDEX IF NOT EXISTS idx_api_keys_group ON api_keys(group_id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records(created_at);
  `)

  ensureSchemaMaintenance(database)
}

export function seedDefaults(database: DatabaseSync): void {
  const now = new Date().toISOString()

  database
    .prepare(`
      INSERT OR IGNORE INTO system_accounts (
        id, username, display_name, description, role, status, password_hash, must_change_password, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'sys_admin',
      'admin',
      '管理员',
      '系统默认管理员账户',
      'admin',
      'active',
      hashPassword('admin'),
      1,
      now,
      now
    )

  const globalStatement = database.prepare(`
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const [key, value] of DEFAULT_GLOBAL_SETTINGS) {
    globalStatement.run(key, JSON.stringify(value), now)
  }

  database.prepare("DELETE FROM global_settings WHERE key IN ('loginTitle', 'loginSubtitle', 'loginBadge')").run()

  database
    .prepare(`
      INSERT OR IGNORE INTO providers (
        id, code, name, description, enabled, base_url, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      OPENAI_PROVIDER_SEED.id,
      OPENAI_PROVIDER_SEED.code,
      OPENAI_PROVIDER_SEED.name,
      OPENAI_PROVIDER_SEED.description,
      OPENAI_PROVIDER_SEED.enabled,
      OPENAI_PROVIDER_SEED.baseUrl,
      JSON.stringify(OPENAI_PROVIDER_SEED.accountTypes),
      JSON.stringify(OPENAI_PROVIDER_SEED.capabilities),
      now,
      now
    )

  seedAdminDefaultOpenAIGroup(database, now)

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }

}

function seedAdminDefaultOpenAIGroup(database: DatabaseSync, timestamp: string): void {
  database
    .prepare(`
      INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
    `)
    .run(
      DEFAULT_OPENAI_GROUP.id,
      DEFAULT_OPENAI_GROUP.systemAccountId,
      DEFAULT_OPENAI_GROUP.name,
      DEFAULT_OPENAI_GROUP.providerCode,
      DEFAULT_OPENAI_GROUP.description,
      timestamp,
      timestamp
    )

  database
    .prepare('UPDATE groups SET is_default = 1 WHERE id = ? AND system_account_id = ?')
    .run(DEFAULT_OPENAI_GROUP.id, DEFAULT_OPENAI_GROUP.systemAccountId)
}


