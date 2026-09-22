package schema

import (
	"context"
	"database/sql"
)

// usage 分片文件的建表语句是 jobs 侧 usagewriter 的权威 DDL 逐字副本。
//
// 依据：projects/jobs/internal/usagewriter/store.go（UsageShardBaseSchemaSQL，
// 镜像 Node backend/src/storage/usage-record-shards.ts
// applyUsageRecordShardBaseSchema，含 legacy index drop）。
//
// 为什么复制而不是 import：Go 三项目基线（docs/migration/Go三项目架构基线.md）
// 禁止 maintenance -> jobs 的依赖，而本地造数写出的 usage 分片必须与 jobs 写出的
// 文件同形——分片缺列或缺索引会让 jobs 的读取/聚合路径在联调时行为不一致。
// 逐字一致性由 sqlite_usage_shard_test.go 的镜像不变量测试对照 jobs 源文本守护，
// 任何一侧改动都会立刻失败。
const sqliteUsageShardBaseDDL = `
    CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      trace_id TEXT NOT NULL,
      traffic_source TEXT NOT NULL,
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      endpoint TEXT,
      provider_code TEXT,
      provider_protocol_profile_id TEXT,
      usage_semantic TEXT,
      model TEXT,
      upstream_model TEXT,
      upstream_response_model TEXT,
      pricing_model TEXT,
      requested_service_tier TEXT NOT NULL DEFAULT 'default',
      effective_service_tier TEXT NOT NULL DEFAULT 'default',
      reported_service_tier TEXT,
      billed_service_tier TEXT NOT NULL DEFAULT 'default',
      requested_reasoning_effort TEXT,
      effective_reasoning_effort TEXT,
      cost_breakdown_snapshot_json TEXT,
      model_mapping_applied INTEGER NOT NULL DEFAULT 0,
      model_mapping_source TEXT,
      source_endpoint_family TEXT,
      upstream_endpoint_family TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      failure_attribution TEXT,
      first_token_ms INTEGER,
      duration_ms INTEGER,
      input_tokens INTEGER,
      output_tokens INTEGER,
      cache_read_tokens INTEGER,
      cache_read_cost_usd REAL,
      cache_write_tokens INTEGER,
      cache_write_1h_tokens INTEGER,
      cache_write_cost_usd REAL,
      thinking_tokens INTEGER,
      input_image_tokens INTEGER,
      output_image_tokens INTEGER,
      input_audio_tokens INTEGER,
      output_audio_tokens INTEGER,
      output_image_count INTEGER,
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
      account_authorization_source_type TEXT,
      account_authorization_source_team_id TEXT,
      group_authorization_id TEXT,
      group_authorization_source_type TEXT,
      group_authorization_source_team_id TEXT,
      created_at TEXT NOT NULL
    );

    DROP INDEX IF EXISTS idx_usage_records_created_at;
    DROP INDEX IF EXISTS idx_usage_records_system_account_created_at;
    DROP INDEX IF EXISTS idx_usage_records_group_real_usage;
    DROP INDEX IF EXISTS idx_usage_records_group_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_first_token_sort;
    DROP INDEX IF EXISTS idx_usage_records_duration_sort;
    DROP INDEX IF EXISTS idx_usage_records_cost_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_first_token_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_duration_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_cost_sort;
    DROP INDEX IF EXISTS idx_usage_records_api_key_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_account_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_trace_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_model_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_model_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_traffic_source_created;
    DROP INDEX IF EXISTS idx_usage_records_client_ip_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_client_ip_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_provider_protocol_profile_created_at;

    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_trace_created_sort ON usage_records(system_account_id, trace_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_group_created_sort ON usage_records(system_account_id, group_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_api_key_created_sort ON usage_records(system_account_id, api_key_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_account_created_sort ON usage_records(system_account_id, account_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);
`

// sqliteUsageShardScript mirrors the single DDL block jobs executes when it
// opens (and creates) one usage shard file.
var sqliteUsageShardScript = sqliteScript{
	sqliteUsageShardBaseDDL,
}

// EnsureSQLiteUsageShard applies the usage shard base schema to one shard file
// (usage-shards/<bucketDateKey>/<shardID>.sqlite3). Every statement is
// IF NOT EXISTS except the legacy index drops, which are no-ops on a fresh
// file, so repeated calls are idempotent.
func EnsureSQLiteUsageShard(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteUsageShardScript.ensure(ctx, db)
}
