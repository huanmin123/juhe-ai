import type { DatabaseSync } from 'node:sqlite'

export function applyUsageCatalogSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;

    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL,
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key);

    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_trace_created_sort ON usage_record_shard_entries(system_account_id, trace_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_api_key_created_sort ON usage_record_shard_entries(system_account_id, api_key_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id);
  `)
}
