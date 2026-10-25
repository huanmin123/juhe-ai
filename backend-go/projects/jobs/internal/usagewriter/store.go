package usagewriter

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ShardStore ports the durable side of the batch write (usage-records
// repository + shard storage): one atomic call per flush batch. The writer
// queue tests run against in-memory mocks of this port; the concrete
// SQLite/Postgres stores below mirror the Node SQL.
type ShardStore interface {
	// WriteBatch persists one flush batch: shard rows, catalog entries and
	// the account side effects, mirroring createUsageRecordsBatch/
	// createUsageRecordsBatchAsync. Returns the number of newly inserted
	// usage rows (ON CONFLICT DO NOTHING misses excluded).
	WriteBatch(ctx Ctx, plan WritePlan) (int, error)
}

// CatalogSchemaSQL mirrors backend/src/storage/schema/usage-catalog-schema.ts
// (applyUsageCatalogSchema).
const CatalogSchemaSQL = `
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
`

// UsageShardBaseSchemaSQL mirrors applyUsageRecordShardBaseSchema
// (usage-record-shards.ts), including the legacy index drops.
const UsageShardBaseSchemaSQL = `
    CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      trace_id TEXT NOT NULL,
      traffic_source TEXT NOT NULL,
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      route_group_id TEXT,
      compaction_expected INTEGER NOT NULL DEFAULT 0,
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
      tts_input_chars INTEGER NOT NULL DEFAULT 0,
      audio_input_seconds REAL NOT NULL DEFAULT 0,
      usage_missing INTEGER NOT NULL DEFAULT 0,
      output_video_seconds REAL NOT NULL DEFAULT 0,
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

var audioMeteringShardColumns = [][2]string{
	{"tts_input_chars", "tts_input_chars INTEGER NOT NULL DEFAULT 0"},
	{"audio_input_seconds", "audio_input_seconds REAL NOT NULL DEFAULT 0"},
	{"usage_missing", "usage_missing INTEGER NOT NULL DEFAULT 0"},
	{"output_video_seconds", "output_video_seconds REAL NOT NULL DEFAULT 0"},
}

var statsMirrorAggregateColumns = []string{
	"id",
	"system_account_id",
	"trace_id",
	"traffic_source",
	"client_ip",
	"api_key_id",
	"group_id",
	"account_id",
	"endpoint",
	"provider_code",
	"provider_protocol_profile_id",
	"model",
	"status_code",
	"success",
	"failure_attribution",
	"first_token_ms",
	"duration_ms",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_read_cost_usd",
	"cache_write_tokens",
	"cache_write_1h_tokens",
	"cache_write_cost_usd",
	"thinking_tokens",
	"input_image_tokens",
	"output_image_tokens",
	"cost_usd",
	"error_code",
	"error_message",
	"account_owner_system_account_id",
	"group_owner_system_account_id",
	"account_access_type",
	"group_access_type",
	"account_authorization_id",
	"account_authorization_source_type",
	"account_authorization_source_team_id",
	"group_authorization_id",
	"group_authorization_source_type",
	"group_authorization_source_team_id",
	"created_at",
}

// statsMirrorParamIndexes / errStatsMirrorColumns 把镜像列映射回行参数
// （UsageRecordColumns 顺序）的下标；init 期解析，列名漂移在首次镜像写时
// 报错而不是写错列。
var (
	statsMirrorParamIndexes []int
	errStatsMirrorColumns   error
)

func sortedAccountIDs(values map[string]string) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

func postgresPlaceholders(count, start int) string {
	parts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		parts = append(parts, "$"+itoa(start+index))
	}
	return strings.Join(parts, ", ")
}

// PostgresShardStoreConfig mirrors the facts the Postgres write path
// consumes (createUsageRecordsBatchPostgres).
type PostgresShardStoreConfig struct {
	// DB is the usage pool client.
	DB *sql.DB
	// ShardCount mirrors runtimeConfig.usageShardCount.
	ShardCount int
	// BusinessDB carries the business pool for the accounts side effect; nil
	// keeps the side effect inside the main transaction like Node does.
	BusinessDB *sql.DB
	// Now substitutes nowIso(); nil = wall clock.
	Now func() time.Time
}

// PostgresShardStore mirrors createUsageRecordsBatchPostgres: one
// transaction locking the side-effect accounts, inserting the usage rows
// (ON CONFLICT(created_at, id) DO NOTHING) and flushing last_used_at.
type PostgresShardStore struct {
	config PostgresShardStoreConfig
	// ensured 是 usage_records 按天分区的进程内备忘（Node 模块级
	// ensuredPartitionDateKeys Set 的等价物；本进程只有一个 store）。
	ensured ensuredPartitionDateKeys
}

// NewPostgresShardStore builds the store.
func NewPostgresShardStore(config PostgresShardStoreConfig) *PostgresShardStore {
	if config.ShardCount < 1 {
		config.ShardCount = DefaultUsageShardCount
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now() }
	}
	return &PostgresShardStore{config: config}
}

// WriteBatch implements ShardStore.
func (s *PostgresShardStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	if len(plan.RowsByShard) == 0 {
		return 0, nil
	}
	lastUsedAt := map[string]string{}
	healthSuccessAt := map[string]string{}
	for _, shardRows := range plan.RowsByShard {
		MergeShardWriteResult(lastUsedAt, healthSuccessAt, shardRows.Rows)
	}

	// ensurePostgresUsageRecordPartitions：插入事务前按批内 createdAt 确保
	// 当日/次日分区存在（autocommit DDL，父表锁窗口不跨事务，与 Node
	// client.execute 一致）。失败带原错误中止本批（writer 保留队头按固定
	// 策略重试）；无效 createdAt 静默跳过，由 INSERT 的 no partition found
	// 原始错误承载，不新增兜底防御。
	createdAts := make([]string, 0, len(plan.RowsByShard)*2)
	for _, shardRows := range plan.RowsByShard {
		for _, row := range shardRows.Rows {
			createdAts = append(createdAts, row.CreatedAt)
		}
	}
	if err := ensurePostgresUsageRecordPartitions(ctx, s.config.DB, &s.ensured, createdAts); err != nil {
		return 0, err
	}

	tx, err := s.config.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	accountIDs := map[string]bool{}
	for id := range lastUsedAt {
		accountIDs[id] = true
	}
	for id := range healthSuccessAt {
		accountIDs[id] = true
	}
	if len(accountIDs) > 0 {
		ids := sortedAccountIDList(accountIDs)
		lockSQL := fmt.Sprintf(`
      SELECT id
      FROM juhe_business.accounts
      WHERE id IN (%s)
        AND deleted_at IS NULL
      ORDER BY id
      FOR NO KEY UPDATE
    `, postgresPlaceholders(len(ids), 1))
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, lockSQL, args...); err != nil {
			return 0, err
		}
	}

	inserted := 0
	columnList := strings.Join(UsageRecordColumns, ", ")
	for _, shardRows := range plan.RowsByShard {
		if len(shardRows.Rows) == 0 {
			continue
		}
		rowPlaceholders := make([]string, 0, len(shardRows.Rows))
		args := make([]any, 0, len(shardRows.Rows)*len(UsageRecordColumns))
		for _, row := range shardRows.Rows {
			rowPlaceholders = append(rowPlaceholders, "("+postgresPlaceholders(len(UsageRecordColumns), len(args)+1)+")")
			args = append(args, row.Params...)
		}
		insertSQL := fmt.Sprintf(`
      INSERT INTO juhe_usage.usage_records (%s)
      VALUES %s
      ON CONFLICT(created_at, id) DO NOTHING
    `, columnList, strings.Join(rowPlaceholders, ", "))
		result, err := tx.ExecContext(ctx, insertSQL, args...)
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil {
			inserted += int(affected)
		}
	}

	for _, accountID := range sortedAccountIDs(lastUsedAt) {
		lastUsed := lastUsedAt[accountID]
		if _, err := tx.ExecContext(ctx, `
      UPDATE juhe_business.accounts
      SET last_used_at = $1, updated_at = $2
      WHERE id = $3
        AND deleted_at IS NULL
        AND (last_used_at IS NULL OR last_used_at < $4)
    `, lastUsed, lastUsed, accountID, lastUsed); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		tx = nil
		return 0, err
	}
	tx = nil
	return inserted, nil
}

func sortedAccountIDList(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
