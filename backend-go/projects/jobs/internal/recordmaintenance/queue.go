// Package recordmaintenance 消费 gateway cleanup POST 写入的
// record_maintenance_jobs 持久交接表。表结构与写入语义的契约在 gateway
// internal/recordmaintenance 包文档：列即 Node RecordMaintenanceJob
// union 中 non_business_data_cleanup 与 account_usage_snapshot_upsert 两个
// 变体的规范化字段并集（id/type/cutoff_at/batch_size/max_batches/created_at
// + v2 扩展 account_id/kind/source/snapshot_json/updated_at），PostgreSQL 落
// juhe_dataset schema（原 SQLite 业务库文件臂已随 sqlite 形态退役删除）。
// 清理行与快照行互斥填充各自列组，其余列写 ” / 0 哨兵；两侧 DDL 逐字一致，
// 对既有旧表各自按列名补 ALTER ADD COLUMN（幂等）。
//
// 队列语义（T207 C1 毒丸修复，对照 Node 本地队列 flush 语义
// modules/record-maintenance/record-maintenance-queue.service.ts 的 FIFO 骨架
// 并替换其 head-of-line 停止分支）：
//   - Dequeue 只取 status='pending' 且 next_attempt_at_ms 到期的行，排序仍按
//     created_at ASC（FIFO 语义保持）；
//   - 瞬时失败：attempt_count+1 且 next_attempt_at_ms 推进指数退避
//     （60s/300s/900s/3600s 封顶），行保留 pending 可重试；
//   - 确定性失败（retention 层 normalize 拒绝类别：未知 type、非法参数，
//     以及快照行未知 kind / snapshot_json 损坏）：status 置 'failed' 终态
//     隔离出队，不再重试；
//   - 所有读面排除 failed 行；表由 data-retention-cleanup 节拍按行龄滚动
//     清理（queue_retention.go），生产者停止后表最终归零。
//
// drain 是 at-least-once：执行成功与删行之间存在窗口，进程在窗口内退出会
// 重复执行同一行；五类维护任务均为幂等清理/快照语义，重复执行无副作用。
package recordmaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// TableName 是交接表名（gateway recordMaintenanceTableName 同名）。
const TableName = "record_maintenance_jobs"

// PGSchema 是 PostgreSQL 方言下交接表所在 schema（gateway DurableDispatch
// 同款 juhe_dataset 前缀）。
const PGSchema = "juhe_dataset"

// schemaSQL 按方言返回建表语句：列定义与 gateway internal/recordmaintenance/
// dispatch.go recordMaintenanceSchema 逐字一致（CREATE IF NOT EXISTS 幂等；
// 不占用生产 migration catalog）。表名经 Table() 方言限定——w12b 门控实测
// 修复前 PG 侧建到 search_path 默认 schema，juhe_dataset 全限定名随后 42P01。
// attempt_count / next_attempt_at_ms / status 是 T207 C1 队列语义列（重试
// 退避与毒丸终态；与 gateway 侧同名同型，升级 DDL 见 retryColumnSQLs）。
func (s *Store) schemaSQL() string {
	return `CREATE TABLE IF NOT EXISTS ` + s.Table() + ` (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  cutoff_at TEXT NOT NULL,
  batch_size INTEGER NOT NULL,
  max_batches INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  snapshot_json TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at_ms BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','failed'))
)`
}

// snapshotColumns 是 v2 契约对既有 6 列旧表的加法式升级列
// （account_usage_snapshot_upsert 载荷；与 gateway 侧
// recordMaintenanceSnapshotColumns 同名同序）。
var snapshotColumns = []string{"account_id", "kind", "source", "snapshot_json", "updated_at"}

// retryColumnSQLs 是 T207 C1 队列语义列对既有表的加法式升级 DDL（逐字一致于
// gateway 侧 recordMaintenanceRetryColumnSQLs；PG 原生 IF NOT EXISTS 幂等，
// 不需要列名预查——status 的 CHECK 约束随列定义一次落库）。
var retryColumnSQLs = []string{
	"ALTER TABLE %s ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE %s ADD COLUMN IF NOT EXISTS next_attempt_at_ms BIGINT NOT NULL DEFAULT 0",
	"ALTER TABLE %s ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','failed'))",
}

// retryBackoffDelaysMs 是瞬时失败的指数退避阶梯：attempt_count=N 的下一次
// 重试延迟取第 N 档，超出档位数取末值封顶（60s/300s/900s/3600s）。Dequeue 的
// 到期判定与 RetryWithBackoff 的推进共用该口径；SQL CASE 字面量与这里逐档
// 对应（retryBackoffDelayMs 供测试与文档锚定）。
var retryBackoffDelaysMs = []int64{60_000, 300_000, 900_000, 3_600_000}

// retryBackoffDelayMs 返回第 attempt 次（1 基）失败后的重试延迟。
func retryBackoffDelayMs(attempt int) int64 {
	if attempt < 1 {
		return retryBackoffDelaysMs[0]
	}
	if attempt > len(retryBackoffDelaysMs) {
		return retryBackoffDelaysMs[len(retryBackoffDelaysMs)-1]
	}
	return retryBackoffDelaysMs[attempt-1]
}

// Store 是 record_maintenance_jobs 交接表的双模读侧（PG dataset pool 句柄由
// 组合根按同源规则提供）。
type Store struct {
	db *sql.DB
	// now 供 Dequeue 的到期判定与退避基准；测试可注入固定时钟。
	now func() time.Time

	mu      sync.Mutex
	ensured bool
}

// OpenStore builds the queue store over an existing handle; the caller owns
// handle lifecycle（组合根的 PG pool 统一关闭）。
func OpenStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return &Store{db: db, now: time.Now}, nil
}

// Table 返回方言限定的表名（PG 走 juhe_dataset schema，SQLite 单库直名）。
func (s *Store) Table() string {
	return PGSchema + "." + TableName
}

// bind 把 ? 占位符改写为 PG 的 $N（gateway DurableDispatch.bind 同语义）。
func (s *Store) bind(query string) string {
	index := 0
	var builder strings.Builder
	for _, char := range query {
		if char == '?' {
			index++
			builder.WriteString("$" + strconv.Itoa(index))
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// EnsureSchema 运行时建表 + 旧表加法式升级（幂等；gateway 侧同样按需建表
// 并补列，先到先建、各自升级，并发双写方以 duplicate column 容错收敛）。
func (s *Store) EnsureSchema(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensured {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, s.bind(s.schemaSQL())); err != nil {
		return fmt.Errorf("初始化 %s schema 失败: %w", s.Table(), err)
	}
	if err := s.ensureSnapshotColumns(ctx); err != nil {
		return fmt.Errorf("升级 %s schema 失败: %w", s.Table(), err)
	}
	for _, statement := range retryColumnSQLs {
		if _, err := s.db.ExecContext(ctx, s.bind(fmt.Sprintf(statement, s.Table()))); err != nil {
			return fmt.Errorf("升级 %s 重试列失败: %w", s.Table(), err)
		}
	}
	s.ensured = true
	return nil
}

// ensureSnapshotColumns 把既有 6 列旧表升级到 v2 契约：按列名补 ALTER ADD
// COLUMN（SQLite 无 ADD COLUMN IF NOT EXISTS；gateway/jobs 两侧同时补同一
// 缺失列时，后到方以 duplicate column 容错；PG 走原生 IF NOT EXISTS）。
func (s *Store) ensureSnapshotColumns(ctx context.Context) error {
	existing, err := s.existingColumns(ctx)
	if err != nil {
		return err
	}
	for _, column := range snapshotColumns {
		if existing[column] {
			continue
		}
		query := "ALTER TABLE " + s.Table() + " ADD COLUMN IF NOT EXISTS " + column + " TEXT NOT NULL DEFAULT ''"
		if _, err := s.db.ExecContext(ctx, s.bind(query)); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}

// existingColumns 按方言列出既有列名（SQLite PRAGMA table_info 的行形状是
// cid/name/type/notnull/dflt_value/pk，取 name；PG 走 information_schema）。
func (s *Store) existingColumns(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT column_name FROM information_schema.columns WHERE table_schema = ? AND table_name = ?`),
		PGSchema, TableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// isDuplicateColumnError 容错并发双写方同时补列的竞态（SQLite
// duplicate column name; PG 原生 IF NOT EXISTS 不会产生该错误）。
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

// Dequeue 按 drain 契约读取队头批次（T207 C1）：只取 status='pending' 且
// next_attempt_at_ms 已到期的行，ORDER BY created_at ASC（gateway 写入的
// created_at 是规范化 UTC ISO 文本，字典序即时间序），id 作确定性
// tiebreaker（与其他清理查询 ORDER BY time, id 先例一致）。failed 行（确定性
// 失败终态）与未到退期的退避行不进读面，失败不再阻塞队头。读取不锁行：
// 单 jobs 进程消费语义与 Node worker 本地队列一致，行在执行成功后才删除。
// v2 列集一并读出：快照行的 account_id/kind/source/snapshot_json/updated_at
// 映射为 retention.RecordMaintenanceJob 的执行器输入（snapshot_json 反序列
// 化为对象；清理行这些列为 ” / 0 哨兵，对应字段留零值）。snapshot_json 损坏
// 属确定性缺陷（JSON 不会自愈）：当场置 failed 隔离出队并跳过该行继续读，
// 不再以整批读失败的形式永久阻塞队头。
func (s *Store) Dequeue(ctx context.Context, limit int) ([]retention.RecordMaintenanceJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, type, cutoff_at, batch_size, max_batches, created_at,
	account_id, kind, source, snapshot_json, updated_at
FROM `+s.Table()+` WHERE status = 'pending' AND next_attempt_at_ms <= ?
ORDER BY created_at ASC, id ASC LIMIT ?`), s.now().UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]retention.RecordMaintenanceJob, 0, limit)
	for rows.Next() {
		var job retention.RecordMaintenanceJob
		var snapshotJSON string
		if err := rows.Scan(&job.ID, &job.Type, &job.CutoffAt, &job.BatchSize, &job.MaxBatches, &job.CreatedAt,
			&job.AccountID, &job.Kind, &job.Source, &snapshotJSON, &job.UpdatedAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(snapshotJSON) != "" {
			snapshot := map[string]any{}
			if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
				if markErr := s.MarkFailed(ctx, job.ID); markErr != nil {
					return nil, markErr
				}
				slog.Warn("数据维护任务 snapshot_json 损坏，已置 failed 隔离出队",
					"event", "record_maintenance_queue_snapshot_corrupt",
					"jobId", job.ID, "jobType", job.Type,
					"payloadSummary", truncateText(snapshotJSON, 200), "error", err)
				continue
			}
			job.Snapshot = snapshot
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// Delete 在 RunOnce 成功后按 id 删除（失败保留等待重试）。行已被并发消费方
// 删除时同样视为成功（at-least-once 去重语义）。
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.Table()+` WHERE id = ?`), id)
	return err
}

// MarkFailed 把确定性失败（retention 层拒绝类别：未知 type、非法参数、未知
// kind、snapshot_json 损坏）的行置为 'failed' 终态：Dequeue 与保留清理之外
// 的所有读面不再选中该行，重试无意义故永久隔离。重复置位幂等。
func (s *Store) MarkFailed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.Table()+` SET status = 'failed' WHERE id = ? AND status = 'pending'`), id)
	return err
}

// RetryWithBackoff 把瞬时失败行推进一轮指数退避（T207 C1）：attempt_count+1，
// next_attempt_at_ms = now + 第 (attempt_count+1) 档延迟（阶梯与封顶见
// retryBackoffDelaysMs）。now 取 Store 时钟（与 Dequeue 到期判定同源，测试
// 可注入）。updated_at 是快照行载荷字段，不得挪作退避记账。行已 failed /
// 已删除时本更新不命中（幂等，at-least-once 并发窗口安全）。
func (s *Store) RetryWithBackoff(ctx context.Context, id string) error {
	// $1 显式 ::bigint：毫秒时间戳超出 int4，未定型参数会被按右端算子的
	// int4 字面量推断，pgx 编码报 int4 溢出。
	query := s.bind(`UPDATE ` + s.Table() + ` SET
	attempt_count = attempt_count + 1,
	next_attempt_at_ms = ?::bigint + CASE attempt_count + 1 WHEN 1 THEN 60000 WHEN 2 THEN 300000 WHEN 3 THEN 900000 ELSE 3600000 END
WHERE id = ? AND status = 'pending'`)
	_, err := s.db.ExecContext(ctx, query, s.now().UnixMilli(), id)
	return err
}

// truncateText 截断超长文本（payload 摘要用）。
func truncateText(value string, maximum int) string {
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}
