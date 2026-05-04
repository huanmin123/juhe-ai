import type { DatabaseSync } from 'node:sqlite'

import { decryptJson, hashPassword, hashSecret } from './crypto.js'

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

  migrateSystemSettingsTable(database)
  database.exec('DROP TABLE IF EXISTS account_authorizations;')
  database.exec('DROP TABLE IF EXISTS group_authorizations;')
  ensureColumn(database, 'global_settings', 'updated_at', 'TEXT NOT NULL')
  migrateSystemTeamsTable(database)

  ensureColumn(database, 'system_accounts', 'must_change_password', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_accounts', 'last_login_at', 'TEXT')
  ensureColumn(database, 'proxy_profiles', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  database.prepare("UPDATE proxy_profiles SET system_account_id = 'sys_admin' WHERE system_account_id <> 'sys_admin'").run()
  ensureColumn(database, 'error_policies', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'accounts', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'accounts', 'credential_fingerprint', 'TEXT')
  ensureColumn(database, 'accounts', 'priority', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'account_expires_at', 'TEXT')
  ensureColumn(database, 'accounts', 'last_used_at', 'TEXT')
  ensureColumn(database, 'accounts', 'cooldown_until', 'TEXT')
  ensureColumn(database, 'accounts', 'last_error_message', 'TEXT')
  ensureColumn(database, 'accounts', 'stream_failure_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'stream_failure_window_started_at', 'TEXT')
  ensureColumn(database, 'system_teams', 'status', "TEXT NOT NULL DEFAULT 'active'")
  ensureColumn(database, 'system_teams', 'description', 'TEXT')
  ensureColumn(database, 'system_team_members', 'member_role', "TEXT NOT NULL DEFAULT 'member'")
  ensureColumn(database, 'system_team_members', 'status', "TEXT NOT NULL DEFAULT 'active'")
  ensureColumn(database, 'system_team_members', 'removed_at', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'remark', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'expires_at', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'limits_json', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'model_policy_json', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'effective_source_type', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'effective_source_team_id', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'activated_at', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'last_source_changed_at', 'TEXT')
  ensureColumn(database, 'resource_authorizations', 'revoked_reason', 'TEXT')
  ensureColumn(database, 'resource_authorization_sources', 'activated_at', 'TEXT')
  ensureColumn(database, 'resource_authorization_sources', 'ended_at', 'TEXT')
  ensureColumn(database, 'resource_authorization_sources', 'ended_reason', 'TEXT')
  ensureColumn(database, 'team_resource_authorization_grants', 'remark', 'TEXT')
  ensureColumn(database, 'team_resource_authorization_grants', 'expires_at', 'TEXT')
  ensureColumn(database, 'team_resource_authorization_grants', 'limits_json', 'TEXT')
  ensureColumn(database, 'team_resource_authorization_grants', 'model_policy_json', 'TEXT')
  ensureColumn(database, 'groups', 'system_account_id', "TEXT NOT NULL DEFAULT 'sys_admin'")
  ensureColumn(database, 'groups', 'provider_code', "TEXT NOT NULL DEFAULT 'openai'")
  ensureColumn(database, 'groups', 'is_default', 'INTEGER NOT NULL DEFAULT 0')
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
  ensureColumn(database, 'usage_records', 'account_owner_system_account_id', 'TEXT')
  ensureColumn(database, 'usage_records', 'group_owner_system_account_id', 'TEXT')
  ensureColumn(database, 'usage_records', 'account_access_type', 'TEXT')
  ensureColumn(database, 'usage_records', 'group_access_type', 'TEXT')
  ensureColumn(database, 'usage_records', 'account_authorization_id', 'TEXT')
  ensureColumn(database, 'usage_records', 'group_authorization_id', 'TEXT')
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
  resetUsageStatsCacheForLocalTimeBuckets(database)
  ensureSystemMetricsColumns(database)
  database.exec('DROP INDEX IF EXISTS idx_accounts_credential_fingerprint;')
  database.exec('DROP INDEX IF EXISTS idx_accounts_owner_credential_fingerprint;')
  cleanupDuplicateSystemTeamNames(database)
  migrateAccountCredentialFingerprints(database)
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status);')
  database.exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active';")
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status);')
  cleanupDuplicateResourceAuthorizations(database)
  cleanupDuplicateResourceAuthorizationSources(database)
  database.exec('DROP INDEX IF EXISTS idx_resource_authorizations_active_unique;')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status);')
  database.exec('CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status);')
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

function cleanupDuplicateResourceAuthorizations(database: DatabaseSync): void {
  const duplicateGroups = database.prepare(`
    SELECT resource_type, resource_id, grantee_system_account_id, MIN(created_at) AS first_created_at, MIN(id) AS keep_id, COUNT(*) AS row_count
    FROM resource_authorizations
    GROUP BY resource_type, resource_id, grantee_system_account_id
    HAVING row_count > 1
  `).all() as unknown as Array<{
    resource_type: string
    resource_id: string
    grantee_system_account_id: string
    keep_id: string
  }>

  for (const group of duplicateGroups) {
    const rows = database.prepare(`
      SELECT id
      FROM resource_authorizations
      WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?
      ORDER BY CASE WHEN status = 'active' THEN 0 ELSE 1 END, created_at ASC, id ASC
    `).all(group.resource_type, group.resource_id, group.grantee_system_account_id) as unknown as Array<{ id: string }>
    const keepId = rows[0]?.id ?? group.keep_id
    const duplicateIds = rows.map((row) => row.id).filter((id) => id !== keepId)
    for (const duplicateId of duplicateIds) {
      database.prepare(`
        UPDATE usage_records
        SET account_authorization_id = ?
        WHERE account_authorization_id = ?
      `).run(keepId, duplicateId)
      database.prepare(`
        UPDATE usage_records
        SET group_authorization_id = ?
        WHERE group_authorization_id = ?
      `).run(keepId, duplicateId)
      database.prepare(`
        UPDATE resource_authorization_sources
        SET authorization_id = ?, updated_at = COALESCE(updated_at, datetime('now'))
        WHERE authorization_id = ?
      `).run(keepId, duplicateId)
      database.prepare('DELETE FROM resource_authorizations WHERE id = ?').run(duplicateId)
    }
  }
}

function cleanupDuplicateSystemTeamNames(database: DatabaseSync): void {
  const columns = database.prepare('PRAGMA table_info(system_teams)').all() as unknown as Array<{ name: string }>
  if (!columns.some((column) => column.name === 'name')) return
  const duplicateNames = database.prepare(`
    SELECT name
    FROM system_teams
    GROUP BY name
    HAVING COUNT(*) > 1
  `).all() as unknown as Array<{ name: string }>
  for (const group of duplicateNames) {
    const rows = database.prepare(`
      SELECT id, name
      FROM system_teams
      WHERE name = ?
      ORDER BY created_at ASC, id ASC
    `).all(group.name) as unknown as Array<{ id: string; name: string }>
    for (const [index, row] of rows.slice(1).entries()) {
      database.prepare('UPDATE system_teams SET name = ?, updated_at = COALESCE(updated_at, datetime(\'now\')) WHERE id = ?').run(`${row.name}-${index + 2}`, row.id)
    }
  }
}

function cleanupDuplicateResourceAuthorizationSources(database: DatabaseSync): void {
  const duplicateGroups = database.prepare(`
    SELECT authorization_id, source_type, COALESCE(source_team_id, '') AS source_team_key, COUNT(*) AS row_count
    FROM resource_authorization_sources
    WHERE status = 'active'
    GROUP BY authorization_id, source_type, COALESCE(source_team_id, '')
    HAVING row_count > 1
  `).all() as unknown as Array<{
    authorization_id: string
    source_type: string
    source_team_key: string
  }>

  for (const group of duplicateGroups) {
    const rows = database.prepare(`
      SELECT id
      FROM resource_authorization_sources
      WHERE authorization_id = ?
        AND source_type = ?
        AND COALESCE(source_team_id, '') = ?
        AND status = 'active'
      ORDER BY activated_at ASC, created_at ASC, id ASC
    `).all(group.authorization_id, group.source_type, group.source_team_key) as unknown as Array<{ id: string }>
    const duplicateIds = rows.slice(1).map((row) => row.id)
    for (const duplicateId of duplicateIds) {
      database.prepare(`
        UPDATE resource_authorization_sources
        SET status = 'revoked',
            ended_at = COALESCE(ended_at, datetime('now')),
            ended_reason = COALESCE(ended_reason, 'duplicate_source_cleaned'),
            revoked_at = COALESCE(revoked_at, datetime('now')),
            updated_at = datetime('now')
        WHERE id = ?
      `).run(duplicateId)
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

function resetUsageStatsCacheForLocalTimeBuckets(database: DatabaseSync): void {
  const migrationKey = '_migration_usage_stats_local_time_buckets_20260505'
  const existingMigration = database.prepare('SELECT key FROM system_settings WHERE system_account_id = ? AND key = ?').get('sys_admin', migrationKey) as unknown
  if (existingMigration) {
    return
  }
  database.exec(`
    DELETE FROM usage_stats_totals;
    DELETE FROM usage_stats_daily;
    DELETE FROM usage_stats_hourly;
    DELETE FROM usage_model_daily;
    DELETE FROM usage_error_daily;
    DELETE FROM usage_stats_clients;
    DELETE FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation';
  `)
  database
    .prepare('INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES (?, ?, ?, ?)')
    .run('sys_admin', migrationKey, JSON.stringify(true), new Date().toISOString())
}

function migrateAccountCredentialFingerprints(database: DatabaseSync): void {
  const rows = database.prepare(`
    SELECT id, provider_code, type, credentials_encrypted, credential_fingerprint
    FROM accounts
    ORDER BY created_at ASC, id ASC
  `).all() as unknown as Array<{
    id: string
    provider_code: string
    type: string
    credentials_encrypted: string
    credential_fingerprint: string | null
  }>
  const firstAccountIdByFingerprint = new Map<string, string>()
  const updateFingerprint = database.prepare('UPDATE accounts SET credential_fingerprint = ? WHERE id = ?')
  const disableDuplicate = database.prepare(`
    UPDATE accounts
    SET credential_fingerprint = NULL,
        status = 'disabled',
        schedulable = 0,
        last_error_message = ?,
        updated_at = ?
    WHERE id = ?
  `)
  const now = new Date().toISOString()

  for (const row of rows) {
    const fingerprint = credentialFingerprintFromEncryptedCredentials(row)
    if (!fingerprint) {
      if (row.credential_fingerprint !== null) {
        updateFingerprint.run(null, row.id)
      }
      continue
    }

    const firstAccountId = firstAccountIdByFingerprint.get(fingerprint)
    if (firstAccountId) {
      disableDuplicate.run(`账户凭据与已有账户 ${firstAccountId} 重复，已在迁移时停用`, now, row.id)
      continue
    }

    firstAccountIdByFingerprint.set(fingerprint, row.id)
    if (row.credential_fingerprint !== fingerprint) {
      updateFingerprint.run(fingerprint, row.id)
    }
  }
}

function credentialFingerprintFromEncryptedCredentials(row: {
  provider_code: string
  type: string
  credentials_encrypted: string
}): string | null {
  try {
    const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    const secret = row.type === 'oauth'
      ? credentials.refresh_token ?? credentials.access_token ?? ''
      : credentials.api_key ?? ''
    return typeof secret === 'string' && secret.trim()
      ? accountCredentialFingerprint(secret)
      : null
  } catch {
    return null
  }
}

function accountCredentialFingerprint(secret: string): string {
  return hashSecret(secret.trim())
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
  ensureColumn(database, 'system_metrics_hourly', 'network_rx_bytes_per_sec_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_bytes_per_sec_sum', 'REAL NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_bytes_per_sec_max', 'REAL')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_bytes_per_sec_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'network_rx_total_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'network_tx_total_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'db_file_bytes_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'stats_lag_seconds_max', 'INTEGER')
  ensureColumn(database, 'system_metrics_hourly', 'sample_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'system_metrics_hourly', 'updated_at', 'TEXT')
  backfillSystemMetricsNetworkCounts(database)
}

function backfillSystemMetricsNetworkCounts(database: DatabaseSync): void {
  database.exec(`
    UPDATE system_metrics_hourly
    SET network_rx_bytes_per_sec_count = COALESCE((
          SELECT COUNT(*)
          FROM system_metrics_samples
          WHERE substr(sampled_at, 1, 13) = system_metrics_hourly.stat_hour
            AND network_rx_bytes_per_sec IS NOT NULL
        ), 0),
        network_tx_bytes_per_sec_count = COALESCE((
          SELECT COUNT(*)
          FROM system_metrics_samples
          WHERE substr(sampled_at, 1, 13) = system_metrics_hourly.stat_hour
            AND network_tx_bytes_per_sec IS NOT NULL
        ), 0)
    WHERE (network_rx_bytes_per_sec_count = 0 AND network_rx_bytes_per_sec_max IS NOT NULL)
       OR (network_tx_bytes_per_sec_count = 0 AND network_tx_bytes_per_sec_max IS NOT NULL);
  `)
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

function migrateSystemTeamsTable(database: DatabaseSync): void {
  const rows = database.prepare('PRAGMA table_info(system_teams)').all() as unknown as Array<{ name: string }>
  if (!rows.length || !rows.some((row) => row.name === 'code')) {
    return
  }

  database.exec(`
    PRAGMA foreign_keys = OFF;
    PRAGMA legacy_alter_table = ON;
    DROP TABLE IF EXISTS system_teams_no_code_migration;
    ALTER TABLE system_teams RENAME TO system_teams_no_code_migration;
    CREATE TABLE system_teams (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      description TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );
    INSERT OR IGNORE INTO system_teams (id, name, description, status, created_by, created_at, updated_at)
    SELECT
      id,
      name,
      description,
      COALESCE(status, 'active'),
      created_by,
      created_at,
      updated_at
    FROM system_teams_no_code_migration;
    DROP TABLE system_teams_no_code_migration;
    PRAGMA legacy_alter_table = OFF;
    PRAGMA foreign_keys = ON;
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

function enforceProviderPassthroughDefaults(database: DatabaseSync): void {
  database.prepare("UPDATE accounts SET passthrough_enabled = 1").run()
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
  markDefaultOpenAIGroups(database)

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
  const streamIdleDefault60Migration = database
    .prepare("SELECT key FROM system_settings WHERE key = '_migration_stream_idle_default_60_20260504'")
    .get() as unknown
  if (!streamIdleDefault60Migration) {
    database
      .prepare("UPDATE system_settings SET value_json = ?, updated_at = ? WHERE system_account_id = 'sys_admin' AND key = 'streamIdleTimeoutSeconds' AND value_json = ?")
      .run(JSON.stringify(60), now, JSON.stringify(30))
    statement.run('sys_admin', '_migration_stream_idle_default_60_20260504', JSON.stringify(true), now)
  }
  database.prepare("DELETE FROM system_settings WHERE key IN ('apiKeyPrefix', 'defaultOpenAIBaseUrl', 'defaultErrorPolicyId', 'defaultAccountConcurrencyLimit', 'streamFailureAction', 'streamAccountCooldownMinutes', 'overloadCooldownEnabled', 'overloadCooldownMinutes')").run()
}

function ensureAdminDefaultOpenAIGroup(database: DatabaseSync, timestamp: string): void {
  ensureDefaultOpenAIGroup(database, 'sys_admin', 'grp_default_openai', timestamp)
}

function migrateDefaultOpenAIGroupForExistingUsers(database: DatabaseSync, timestamp: string): void {
  const migrationKey = '_migration_default_openai_group_per_system_account_20260503'
  ensureDefaultOpenAIGroups(database, timestamp)

  const existingMigration = database.prepare('SELECT key FROM system_settings WHERE system_account_id = ? AND key = ?').get('sys_admin', migrationKey) as unknown
  if (existingMigration) {
    return
  }

  database.prepare('INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES (?, ?, ?, ?)').run('sys_admin', migrationKey, JSON.stringify(true), timestamp)
}

function markDefaultOpenAIGroups(database: DatabaseSync): void {
  database
    .prepare('UPDATE groups SET is_default = 1 WHERE provider_code = ? AND name = ?')
    .run('openai', DEFAULT_OPENAI_GROUP_NAME)
}

function ensureDefaultOpenAIGroups(database: DatabaseSync, timestamp: string): void {
  const systemAccounts = database.prepare('SELECT id FROM system_accounts').all() as unknown as Array<{ id: string }>

  for (const account of systemAccounts) {
    const groupId = account.id === 'sys_admin' ? 'grp_default_openai' : `grp_default_openai_${account.id}`
    ensureDefaultOpenAIGroup(database, account.id, groupId, timestamp)
  }
}

function ensureDefaultOpenAIGroup(database: DatabaseSync, systemAccountId: string, preferredGroupId: string, timestamp: string): void {
  const existing = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND name = ? LIMIT 1')
    .get(systemAccountId, 'openai', DEFAULT_OPENAI_GROUP_NAME) as unknown as { id?: string } | undefined
  const markDefaultGroup = database.prepare('UPDATE groups SET is_default = 1 WHERE id = ? AND system_account_id = ?')
  if (existing?.id) {
    markDefaultGroup.run(existing.id, systemAccountId)
    return
  }

  const insertGroup = database.prepare(`
    INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
  `)
  const fallbackGroupId = systemAccountId === 'sys_admin' ? 'grp_default_openai_sys_admin' : `grp_default_openai_${systemAccountId}`
  const candidateIds = [...new Set([preferredGroupId, fallbackGroupId])]

  for (const candidateId of candidateIds) {
    const result = insertGroup.run(candidateId, systemAccountId, DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
    if (result.changes > 0) {
      markDefaultGroup.run(candidateId, systemAccountId)
      return
    }
  }

  for (let attempt = 0; attempt < 5; attempt += 1) {
    const candidateId = `${fallbackGroupId}_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 10)}`
    const result = insertGroup.run(candidateId, systemAccountId, DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
    if (result.changes > 0) {
      markDefaultGroup.run(candidateId, systemAccountId)
      return
    }
  }

  throw new Error(`无法创建 ${systemAccountId} 的默认 OpenAI 分组`)
}


