package usagewriter

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ShardStore ports the durable side of the batch write: one atomic call per
// flush batch. The writer queue tests run against in-memory mocks of this
// port. The PostgreSQL store writes only juhe_usage.usage_records rows plus
// account side effects; the retired usage_record_shard_* catalog tables are
// not maintained by the gateway/jobs write path.
type ShardStore interface {
	// WriteBatch persists one flush batch: usage rows and the account side
	// effects, mirroring createUsageRecordsBatch /
	// createUsageRecordsBatchAsync. Returns the number of newly inserted
	// usage rows (ON CONFLICT DO NOTHING misses excluded).
	WriteBatch(ctx Ctx, plan WritePlan) (int, error)
}

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
	if err := ensurePostgresUsageRecordPartitionsChecked(ctx, s.config.DB, &s.ensured, createdAts, s.usageRecordPartitionExists); err != nil {
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
			if isMissingUsageRecordPartition(err) {
				// C2-H3：分区在 ensure 之后被保留策略 DROP。清掉批内日期键
				// 备忘，让上游重试重建分区；否则备忘会一直短路、整批正常
				// 记录在固定重试预算耗尽后一起进死信。
				for _, dateKey := range usageRecordPartitionDateKeys(createdAts) {
					s.ensured.forget(dateKey)
				}
			}
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil {
			inserted += int(affected)
		}
	}

	if err := tx.Commit(); err != nil {
		tx = nil
		return 0, err
	}
	tx = nil

	// accounts.last_used_at 是运营性副作用（非账务事实）：放在 usage 事务
	// 提交之后逐账户短 UPDATE，避免把业务账户行锁横跨整批 INSERT/提交
	//（2026-11-09 非业务数据性能优先裁决）。失败不影响已提交的 usage 行，
	// 也不重试整批（最多晚一次刷新，可接受）。
	for _, accountID := range sortedAccountIDs(lastUsedAt) {
		lastUsed := lastUsedAt[accountID]
		_, _ = s.config.DB.ExecContext(ctx, `
      UPDATE juhe_business.accounts
      SET last_used_at = $1, updated_at = $2
      WHERE id = $3
        AND deleted_at IS NULL
        AND (last_used_at IS NULL OR last_used_at < $4)
    `, lastUsed, lastUsed, accountID, lastUsed)
	}
	return inserted, nil
}

// usageRecordPartitionExists 复核日期分区是否仍是 usage_records 的真实分区：
// dataretention 会 DETACH/DROP 过期分区，进程内备忘因此不能作为不可证伪的
// 真相（C2-H3）。relispartition 同时排除同名普通表。
func (s *PostgresShardStore) usageRecordPartitionExists(ctx Ctx, partitionName string) (bool, error) {
	var exists bool
	err := s.config.DB.QueryRowContext(ctx, `
    SELECT EXISTS (
      SELECT 1
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'juhe_usage'
        AND c.relname = $1
        AND c.relispartition
    )
  `, partitionName).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}
