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
      concurrency_limit INTEGER NOT NULL DEFAULT 1,
      passthrough_enabled INTEGER NOT NULL DEFAULT 0,
      error_policy_id TEXT,
      priority INTEGER NOT NULL DEFAULT 0,
      schedulable INTEGER NOT NULL DEFAULT 1,
      notes TEXT,
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

    CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS group_accounts (
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      weight INTEGER NOT NULL DEFAULT 1,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (group_id, account_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL,
      key_hash TEXT NOT NULL UNIQUE,
      key_prefix TEXT NOT NULL,
      key_secret_encrypted TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      group_id TEXT NOT NULL,
      expires_at TEXT,
      rate_limit INTEGER,
      quota_limit INTEGER,
      scopes_json TEXT NOT NULL DEFAULT '[]',
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (group_id) REFERENCES groups(id)
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
      network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0,
      network_tx_bytes_per_sec_max REAL,
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

  migrateSystemSettingsTable(database)
  ensureColumn(database, 'global_settings', 'updated_at', 'TEXT NOT NULL')

  ensureColumn(database, 'system_accounts', 'must_change_password', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_accounts', 'last_login_at', 'TEXT')
  ensureColumn(database, 'proxy_profiles', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'error_policies', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'accounts', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'accounts', 'credential_fingerprint', 'TEXT')
  ensureColumn(database, 'accounts', 'priority', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'last_used_at', 'TEXT')
  ensureColumn(database, 'accounts', 'cooldown_until', 'TEXT')
  ensureColumn(database, 'accounts', 'last_error_message', 'TEXT')
  ensureColumn(database, 'accounts', 'stream_failure_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'stream_failure_window_started_at', 'TEXT')
  ensureColumn(database, 'groups', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'groups', 'provider_code', "TEXT NOT NULL DEFAULT 'openai'")
  ensureColumn(database, 'group_accounts', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'api_keys', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'api_keys', 'key_secret_encrypted', 'TEXT')
  ensureColumn(database, 'usage_records', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'usage_records', 'client_ip', 'TEXT')
  ensureColumn(database, 'usage_records', 'endpoint', 'TEXT')
  ensureColumn(database, 'usage_records', 'first_token_ms', 'INTEGER')
  ensureColumn(database, 'usage_records', 'input_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'output_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'cache_read_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'cost_usd', 'REAL')
  ensureColumn(database, 'usage_records', 'request_snapshot_json', 'TEXT')
  ensureColumn(database, 'usage_records', 'response_snapshot_json', 'TEXT')
  ensureColumn(database, 'account_usage_snapshots', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'account_usage_snapshots', 'refresh_status', 'TEXT')
  ensureColumn(database, 'account_usage_snapshots', 'last_attempt_at', 'TEXT')
  ensureColumn(database, 'account_usage_snapshots', 'last_success_at', 'TEXT')
  ensureColumn(database, 'account_usage_snapshots', 'next_refresh_after', 'TEXT')
  ensureColumn(database, 'account_usage_snapshots', 'last_error_message', 'TEXT')
  migrateAccountUsageSnapshotsTable(database)
  ensureColumn(database, 'stats_job_state', 'cursor_created_at', 'TEXT')
  ensureColumn(database, 'stats_job_state', 'cursor_id', 'TEXT')
  ensureColumn(database, 'stats_job_state', 'last_success_at', 'TEXT')
  ensureColumn(database, 'stats_job_state', 'last_error_message', 'TEXT')
  ensureColumn(database, 'stats_job_state', 'lag_seconds', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'stats_job_state', 'updated_at', 'TEXT')
  ensureUsageStatsColumns(database)
  migrateUsageStatsLegacyColumns(database)
  migrateStatsJobStateLegacyColumns(database)
  ensureSystemMetricsColumns(database)
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);')
  database.exec('DROP INDEX IF EXISTS idx_accounts_credential_fingerprint;')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_credential_fingerprint ON accounts(system_account_id, credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_api_keys_system_account ON api_keys(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_at ON usage_records(system_account_id, created_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind ON account_usage_snapshots(kind, updated_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_model ON usage_model_daily(system_account_id, stat_date, model);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_sampled_at ON system_metrics_samples(sampled_at);')
}

function ensureUsageStatsColumns(database: DatabaseSync): void {
  for (const tableName of ['usage_stats_totals', 'usage_stats_daily'] as const) {
    ensureColumn(database, tableName, 'client_count', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'total_cost_usd', 'REAL NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'duration_ms_sum', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'duration_ms_count', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'first_token_ms_sum', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'first_token_ms_count', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'last_used_at', 'TEXT')
    ensureColumn(database, tableName, 'last_error_at', 'TEXT')
    ensureColumn(database, tableName, 'updated_at', 'TEXT')
  }

  for (const tableName of ['usage_stats_hourly', 'usage_model_daily'] as const) {
    ensureColumn(database, tableName, 'total_cost_usd', 'REAL NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'duration_ms_sum', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'duration_ms_count', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'first_token_ms_sum', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'first_token_ms_count', 'INTEGER NOT NULL DEFAULT 0')
    ensureColumn(database, tableName, 'updated_at', 'TEXT')
  }

  ensureColumn(database, 'usage_model_daily', 'success_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'usage_model_daily', 'error_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'usage_model_daily', 'provider_code', "TEXT NOT NULL DEFAULT 'unknown'")
  ensureColumn(database, 'usage_error_daily', 'provider_code', "TEXT NOT NULL DEFAULT 'unknown'")
  ensureColumn(database, 'usage_error_daily', 'error_group', "TEXT NOT NULL DEFAULT 'unknown'")
  ensureColumn(database, 'usage_error_daily', 'status_code', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'usage_error_daily', 'error_message', 'TEXT')
  ensureColumn(database, 'usage_error_daily', 'request_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'usage_error_daily', 'updated_at', 'TEXT')
}

function migrateUsageStatsLegacyColumns(database: DatabaseSync): void {
  const usageStatsTables = ['usage_stats_totals', 'usage_stats_daily', 'usage_stats_hourly', 'usage_model_daily'] as const
  for (const tableName of usageStatsTables) {
    const columns = database.prepare(`PRAGMA table_info(${tableName})`).all() as unknown as Array<{ name: string }>
    const hasLegacyTotalCost = columns.some((row) => row.name === 'total_cost')
    const hasLegacyClientCount = columns.some((row) => row.name === 'distinct_client_count')
    if (hasLegacyTotalCost) {
      database.exec(`UPDATE ${tableName} SET total_cost_usd = COALESCE(total_cost_usd, total_cost) WHERE total_cost_usd = 0 AND total_cost IS NOT NULL;`)
    }
    if (hasLegacyClientCount && tableName !== 'usage_model_daily' && tableName !== 'usage_stats_hourly') {
      database.exec(`UPDATE ${tableName} SET client_count = COALESCE(client_count, distinct_client_count) WHERE client_count = 0 AND distinct_client_count IS NOT NULL;`)
    }
  }
}

function migrateStatsJobStateLegacyColumns(database: DatabaseSync): void {
  const columns = database.prepare('PRAGMA table_info(stats_job_state)').all() as unknown as Array<{ name: string }>
  const hasLegacyCursorCreatedAt = columns.some((row) => row.name === 'last_usage_created_at')
  const hasLegacyCursorId = columns.some((row) => row.name === 'last_usage_id')
  if (!hasLegacyCursorCreatedAt && !hasLegacyCursorId) {
    return
  }
  database.exec(`
    UPDATE stats_job_state
    SET cursor_created_at = COALESCE(cursor_created_at, last_usage_created_at),
        cursor_id = COALESCE(cursor_id, last_usage_id)
    WHERE (cursor_created_at IS NULL OR cursor_id IS NULL)
      AND (last_usage_created_at IS NOT NULL OR last_usage_id IS NOT NULL);
  `)
}

function ensureSystemMetricsColumns(database: DatabaseSync): void {
  ensureColumn(database, 'system_metrics_samples', 'id', 'TEXT')
  ensureColumn(database, 'system_metrics_samples', 'created_at', 'TEXT')
  ensureColumn(database, 'system_metrics_samples', 'network_rx_bytes_per_sec', 'REAL')
  ensureColumn(database, 'system_metrics_samples', 'network_tx_bytes_per_sec', 'REAL')
  ensureColumn(database, 'system_metrics_samples', 'network_rx_total_bytes', 'INTEGER')
  ensureColumn(database, 'system_metrics_samples', 'network_tx_total_bytes', 'INTEGER')

  ensureColumn(database, 'system_metrics_hourly', 'cpu_percent_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'memory_used_percent_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'process_rss_bytes_sum', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'process_heap_used_bytes_sum', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'process_heap_used_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'event_loop_lag_ms_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_rx_bytes_per_sec_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_rx_bytes_per_sec_max', 'REAL')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_bytes_per_sec_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_bytes_per_sec_max', 'REAL')
  ensureColumn(database, 'system_metrics_hourly', 'network_rx_total_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_total_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'db_file_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'stats_lag_seconds_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'sample_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'updated_at', 'TEXT')
}

function migrateAccountUsageSnapshotsTable(database: DatabaseSync): void {
  const rows = database.prepare('PRAGMA table_info(account_usage_snapshots)').all() as unknown as Array<{ name: string; pk: number }>
  const pkColumns = rows.filter((row) => row.pk > 0).sort((left, right) => left.pk - right.pk).map((row) => row.name)
  if (pkColumns.join(',') === 'system_account_id,account_id,kind') {
    return
  }

  database.exec(`
    DROP TABLE IF EXISTS account_usage_snapshots_pk_migration;
    ALTER TABLE account_usage_snapshots RENAME TO account_usage_snapshots_pk_migration;
    CREATE TABLE account_usage_snapshots (
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
    INSERT OR IGNORE INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    )
    SELECT
      COALESCE(system_account_id, 'sys_admin'), account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    FROM account_usage_snapshots_pk_migration;
    DROP TABLE account_usage_snapshots_pk_migration;
  `)
}

function migrateSystemSettingsTable(database: DatabaseSync): void {
  const rows = database.prepare('PRAGMA table_info(system_settings)').all() as unknown as Array<{ name: string; pk: number }>
  if (!rows.length || rows.some((row) => row.name === 'system_account_id' && row.pk > 0)) {
    return
  }
  if (!rows.some((row) => row.name === 'system_account_id')) {
    database.exec(`
      DROP TABLE IF EXISTS system_settings_legacy_single;
      ALTER TABLE system_settings RENAME TO system_settings_legacy_single;
      CREATE TABLE system_settings (
        system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
        key TEXT NOT NULL,
        value_json TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        PRIMARY KEY (system_account_id, key)
      );
      INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
      SELECT 'sys_admin', key, value_json, updated_at FROM system_settings_legacy_single;
      DROP TABLE system_settings_legacy_single;
    `)
    return
  }

  database.exec(`
    DROP TABLE IF EXISTS system_settings_legacy_single;
    ALTER TABLE system_settings RENAME TO system_settings_legacy_single;
    CREATE TABLE system_settings (
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      key TEXT NOT NULL,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, key)
    );
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    SELECT COALESCE(system_account_id, 'sys_admin'), key, value_json, updated_at FROM system_settings_legacy_single;
    DROP TABLE system_settings_legacy_single;
  `)
}

function ensureColumn(database: DatabaseSync, tableName: string, columnName: string, columnDefinition: string): void {
  const rows = database.prepare(`PRAGMA table_info(${tableName})`).all() as unknown as Array<{ name: string }>
  if (!rows.some((row) => row.name === columnName)) {
    database.exec(`ALTER TABLE ${tableName} ADD COLUMN ${columnName} ${columnDefinition};`)
  }
}

export function seedDefaults(database: DatabaseSync): void {
  const now = new Date().toISOString()

  database
    .prepare(`
      INSERT OR IGNORE INTO system_accounts (
        id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'sys_admin',
      'admin',
      '管理员',
      'admin',
      'active',
      hashPassword('admin'),
      1,
      now,
      now
    )

  const globalSettings = [
    ['appName', '聚合 AI'],
    ['appIcon', '/brand-icon.svg'],
    ['loginTitle', '聚合 AI 管理平台'],
    ['loginSubtitle', '统一接入、统一调度、统一可观测。'],
    ['loginBadge', '统一接入平台']
  ] as const

  const globalStatement = database.prepare(`
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const [key, value] of globalSettings) {
    globalStatement.run(key, JSON.stringify(value), now)
  }

  database.prepare("UPDATE global_settings SET value_json = ?, updated_at = ? WHERE key = 'loginSubtitle' AND value_json = ?")
    .run(JSON.stringify('统一接入、统一调度、统一可观测。'), now, JSON.stringify('统一登录、统一调度、统一可观测。'))
  database.prepare("UPDATE global_settings SET value_json = ?, updated_at = ? WHERE key = 'loginBadge' AND value_json = ?")
    .run(JSON.stringify('统一接入平台'), now, JSON.stringify('AI Control Plane'))

  database
    .prepare(`
      INSERT OR IGNORE INTO providers (
        id, code, name, enabled, base_url, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'openai',
      'openai',
      'OpenAI',
      1,
      'https://api.openai.com/v1',
      JSON.stringify(['oauth', 'api_key']),
      JSON.stringify(['models', 'responses', 'stream', 'passthrough']),
      now,
      now
    )

  ensureAdminDefaultOpenAIGroup(database, now)
  migrateDefaultOpenAIGroupForExistingUsers(database, now)

  const settings = [
    ['appName', '聚合 AI'],
    ['appIcon', '/brand-icon.svg'],
    ['defaultAccountConcurrencyLimit', 3],
    ['defaultTemporaryUnschedulableMinutes', 5],
    ['temporaryUnschedulableRetryIntervalSeconds', 3],
    ['temporaryUnschedulableRetryAttempts', 3],
    ['streamCircuitBreakerEnabled', true],
    ['streamRequestTimeoutSeconds', 180],
    ['streamIdleTimeoutSeconds', 30],
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

  const streamDefaultMigration = database
    .prepare("SELECT key FROM system_settings WHERE key = '_migration_stream_circuit_default_enabled_20260502'")
    .get() as unknown
  if (!streamDefaultMigration) {
    statement.run('sys_admin', 'streamCircuitBreakerEnabled', JSON.stringify(true), now)
    statement.run('sys_admin', '_migration_stream_circuit_default_enabled_20260502', JSON.stringify(true), now)
  }
  const streamIdleDefaultMigration = database
    .prepare("SELECT key FROM system_settings WHERE key = '_migration_stream_idle_default_30_20260502'")
    .get() as unknown
  if (!streamIdleDefaultMigration) {
    database
      .prepare("UPDATE system_settings SET value_json = ?, updated_at = ? WHERE system_account_id = 'sys_admin' AND key = 'streamIdleTimeoutSeconds' AND value_json = ?")
      .run(JSON.stringify(30), now, JSON.stringify(180))
    statement.run('sys_admin', '_migration_stream_idle_default_30_20260502', JSON.stringify(true), now)
  }
  database.prepare("DELETE FROM system_settings WHERE system_account_id = 'sys_admin' AND key IN ('apiKeyPrefix', 'defaultOpenAIBaseUrl', 'defaultErrorPolicyId', 'streamFailureAction', 'streamAccountCooldownMinutes', 'overloadCooldownEnabled', 'overloadCooldownMinutes')").run()
}

function ensureAdminDefaultOpenAIGroup(database: DatabaseSync, timestamp: string): void {
  database
    .prepare(`
      INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, ?, ?)
    `)
    .run('grp_default_openai', 'sys_admin', DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
}

function migrateDefaultOpenAIGroupForExistingUsers(database: DatabaseSync, timestamp: string): void {
  const migrationKey = '_migration_default_openai_group_per_system_account_20260503'
  const existingMigration = database.prepare('SELECT key FROM system_settings WHERE system_account_id = ? AND key = ?').get('sys_admin', migrationKey) as unknown
  if (existingMigration) {
    return
  }

  ensureDefaultOpenAIGroups(database, timestamp)
  database.prepare('INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES (?, ?, ?, ?)').run('sys_admin', migrationKey, JSON.stringify(true), timestamp)
}

function ensureDefaultOpenAIGroups(database: DatabaseSync, timestamp: string): void {
  const systemAccounts = database.prepare('SELECT id FROM system_accounts').all() as unknown as Array<{ id: string }>
  const findGroup = database.prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND name = ? LIMIT 1')
  const insertGroup = database.prepare(`
    INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, 1, ?, ?)
  `)

  for (const account of systemAccounts) {
    const existing = findGroup.get(account.id, 'openai', DEFAULT_OPENAI_GROUP_NAME) as unknown
    if (existing) {
      continue
    }
    const groupId = account.id === 'sys_admin' ? 'grp_default_openai' : `grp_default_openai_${account.id}`
    insertGroup.run(groupId, account.id, DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
  }
}


