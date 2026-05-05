import type { DatabaseSync } from 'node:sqlite'

import { hashPassword } from './crypto.js'

const DEFAULT_OPENAI_GROUP_NAME = '默认 OpenAI 分组'
const DEFAULT_OPENAI_GROUP_DESCRIPTION = '第一期默认分组'

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
      request_id TEXT NOT NULL,
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
      client_count INTEGER NOT NULL DEFAULT 0,
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
      client_count INTEGER NOT NULL DEFAULT 0,
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

    CREATE TABLE IF NOT EXISTS usage_stats_clients (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      stat_bucket TEXT NOT NULL,
      client_key TEXT NOT NULL,
      first_seen_at TEXT NOT NULL,
      last_seen_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id, stat_bucket, client_key)
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

  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_group_account_stats_group ON group_account_stats(group_id);')
  ensureColumn(database, 'group_accounts', 'account_authorization_id', 'TEXT')
  ensureColumn(database, 'api_keys', 'group_authorization_id', 'TEXT')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username));')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name));')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique_lower ON system_teams(lower(name));')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status);')
  database.exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active';")
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_api_keys_group_authorization ON api_keys(group_authorization_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_team ON team_resource_authorization_grants(team_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_resource ON team_resource_authorization_grants(resource_type, resource_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_owner ON team_resource_authorization_grants(resource_owner_system_account_id, status);')
  database.exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_active_unique ON team_resource_authorization_grants(resource_type, resource_id, team_id) WHERE status = 'active';")
  database.exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual';")
  database.exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team';")
  database.exec('CREATE INDEX IF NOT EXISTS idx_api_keys_system_account ON api_keys(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_at ON usage_records(system_account_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_first_token_sort ON usage_records(first_token_ms, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_duration_sort ON usage_records(duration_ms, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_cost_sort ON usage_records(cost_usd, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_first_token_sort ON usage_records(system_account_id, first_token_ms, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_duration_sort ON usage_records(system_account_id, duration_ms, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_cost_sort ON usage_records(system_account_id, cost_usd, created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind ON account_usage_snapshots(kind, updated_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date ON usage_stats_daily(stat_date);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_hour ON usage_stats_hourly(stat_hour);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_model_daily_stat_date ON usage_model_daily(stat_date);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_provider_model ON usage_model_daily(system_account_id, stat_date, provider_code, model);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_error_daily_stat_date ON usage_error_daily(stat_date);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_clients_bucket ON usage_stats_clients(stat_bucket);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_sampled_at ON system_metrics_samples(sampled_at);')
}

function ensureColumn(database: DatabaseSync, tableName: string, columnName: string, columnType: string): void {
  const rows = database.prepare(`PRAGMA table_info(${tableName})`).all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === columnName)) {
    return
  }
  database.exec(`ALTER TABLE ${tableName} ADD COLUMN ${columnName} ${columnType}`)
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

  const globalSettings = [
    ['appName', '聚合 AI'],
    ['appIcon', '/brand-icon.svg']
  ] as const

  const globalStatement = database.prepare(`
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const [key, value] of globalSettings) {
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
      'openai',
      'openai',
      'OpenAI',
      '第一阶段内置供应商，支持 OAuth 与 API Key 两种账户接入方式',
      1,
      'https://api.openai.com/v1',
      JSON.stringify(['oauth', 'api_key']),
      JSON.stringify(['models', 'responses', 'stream', 'passthrough']),
      now,
      now
    )

  seedAdminDefaultOpenAIGroup(database, now)

  const settings = [
    ['appName', '聚合 AI'],
    ['appIcon', '/brand-icon.svg'],
    ['defaultTemporaryUnschedulableMinutes', 5],
    ['temporaryUnschedulableRetryIntervalSeconds', 3],
    ['temporaryUnschedulableRetryAttempts', 3],
    ['streamCircuitBreakerEnabled', true],
    ['streamRequestTimeoutSeconds', 180],
    ['streamIdleTimeoutSeconds', 60],
    ['streamFailureThresholdCount', 3],
    ['streamFailureThresholdWindowMinutes', 10],
  ] as const

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of settings) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }

}

function seedAdminDefaultOpenAIGroup(database: DatabaseSync, timestamp: string): void {
  database
    .prepare(`
      INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
    `)
    .run('grp_default_openai_sys_admin', 'sys_admin', DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)

  database
    .prepare('UPDATE groups SET is_default = 1 WHERE id = ? AND system_account_id = ?')
    .run('grp_default_openai_sys_admin', 'sys_admin')
}


