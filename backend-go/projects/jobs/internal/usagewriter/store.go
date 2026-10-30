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
