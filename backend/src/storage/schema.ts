import type { DatabaseSync } from 'node:sqlite'

export function applySchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;
    PRAGMA journal_mode = WAL;

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
      name TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      rules_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
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
      name TEXT NOT NULL,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS group_accounts (
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

    CREATE TABLE IF NOT EXISTS system_settings (
      key TEXT PRIMARY KEY,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status);
    CREATE INDEX IF NOT EXISTS idx_api_keys_group ON api_keys(group_id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records(created_at);
  `)

  ensureColumn(database, 'accounts', 'credential_fingerprint', 'TEXT')
  ensureColumn(database, 'accounts', 'priority', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'last_used_at', 'TEXT')
  ensureColumn(database, 'accounts', 'cooldown_until', 'TEXT')
  ensureColumn(database, 'accounts', 'last_error_message', 'TEXT')
  ensureColumn(database, 'accounts', 'stream_failure_count', 'INTEGER NOT NULL DEFAULT 0')
  ensureColumn(database, 'accounts', 'stream_failure_window_started_at', 'TEXT')
  ensureColumn(database, 'groups', 'provider_code', "TEXT NOT NULL DEFAULT 'openai'")
  ensureColumn(database, 'api_keys', 'key_secret_encrypted', 'TEXT')
  ensureColumn(database, 'usage_records', 'client_ip', 'TEXT')
  ensureColumn(database, 'usage_records', 'endpoint', 'TEXT')
  ensureColumn(database, 'usage_records', 'first_token_ms', 'INTEGER')
  ensureColumn(database, 'usage_records', 'input_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'output_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'cache_read_tokens', 'INTEGER')
  ensureColumn(database, 'usage_records', 'cost_usd', 'REAL')
  ensureColumn(database, 'usage_records', 'request_snapshot_json', 'TEXT')
  ensureColumn(database, 'usage_records', 'response_snapshot_json', 'TEXT')
  database.exec('CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);')
  database.exec('CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;')
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

  database
    .prepare(`
      INSERT OR IGNORE INTO groups (id, name, provider_code, description, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?)
    `)
    .run('grp_default_openai', '默认 OpenAI 分组', 'openai', '第一期默认分组', 1, now, now)

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
    INSERT OR IGNORE INTO system_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)

  for (const [key, value] of settings) {
    statement.run(key, JSON.stringify(value), now)
  }

  const streamDefaultMigration = database
    .prepare("SELECT key FROM system_settings WHERE key = '_migration_stream_circuit_default_enabled_20260502'")
    .get() as unknown
  if (!streamDefaultMigration) {
    statement.run('streamCircuitBreakerEnabled', JSON.stringify(true), now)
    statement.run('_migration_stream_circuit_default_enabled_20260502', JSON.stringify(true), now)
  }
  const streamIdleDefaultMigration = database
    .prepare("SELECT key FROM system_settings WHERE key = '_migration_stream_idle_default_30_20260502'")
    .get() as unknown
  if (!streamIdleDefaultMigration) {
    database
      .prepare("UPDATE system_settings SET value_json = ?, updated_at = ? WHERE key = 'streamIdleTimeoutSeconds' AND value_json = ?")
      .run(JSON.stringify(30), now, JSON.stringify(180))
    statement.run('_migration_stream_idle_default_30_20260502', JSON.stringify(true), now)
  }
  database.prepare("DELETE FROM system_settings WHERE key IN ('apiKeyPrefix', 'defaultOpenAIBaseUrl', 'defaultErrorPolicyId', 'streamFailureAction', 'streamAccountCooldownMinutes', 'overloadCooldownEnabled', 'overloadCooldownMinutes')").run()
}
