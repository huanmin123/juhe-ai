// Package recordmaintenance 消费 gateway cleanup POST 写入的
// record_maintenance_jobs 持久交接表。表结构与写入语义的契约在 gateway
// internal/tablemonitor/enqueue.go 包文档：列即 Node RecordMaintenanceJob
// union 中 non_business_data_cleanup 与 account_usage_snapshot_upsert 两个
// 变体的规范化字段并集（id/type/cutoff_at/batch_size/max_batches/created_at
// + v2 扩展 account_id/kind/source/snapshot_json/updated_at），gateway 侧
// SQLite 落业务库文件、PostgreSQL 落 juhe_dataset schema。清理行与快照行
// 互斥填充各自列组，其余列写 ” / 0 哨兵；两侧 DDL 逐字一致，对既有 6 列
// 旧表各自按列名补 ALTER ADD COLUMN（幂等）。
//
// jobs 侧 drain 契约（对照 Node 本地队列 flush 语义
// modules/record-maintenance/record-maintenance-queue.service.ts）：
// ORDER BY created_at 读取、RunOnce 成功后按 id 删除、失败保留等待重试。
// drain 是 at-least-once：执行成功与删行之间存在窗口，进程在窗口内退出会
// 重复执行同一行；五类维护任务均为幂等清理/快照语义，重复执行无副作用。
package recordmaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// TableName 是交接表名（gateway recordMaintenanceTableName 同名）。
const TableName = "record_maintenance_jobs"

// PGSchema 是 PostgreSQL 方言下交接表所在 schema（gateway DurableDispatch
// 同款 juhe_dataset 前缀）。
const PGSchema = "juhe_dataset"

// schema 与 gateway internal/tablemonitor/enqueue.go recordMaintenanceSchema
// 逐字一致（两侧各自运行时建表，CREATE IF NOT EXISTS 幂等；不占用生产
// migration catalog，与 accounttesttask schema 先例一致）。
var schema = `CREATE TABLE IF NOT EXISTS record_maintenance_jobs (
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
  updated_at TEXT NOT NULL DEFAULT ''
)`

// snapshotColumns 是 v2 契约对既有 6 列旧表的加法式升级列
// （account_usage_snapshot_upsert 载荷；与 gateway 侧
// recordMaintenanceSnapshotColumns 同名同序）。
var snapshotColumns = []string{"account_id", "kind", "source", "snapshot_json", "updated_at"}

// Store 是 record_maintenance_jobs 交接表的双模读侧（SQLite 业务库句柄 /
// PG dataset pool 句柄由组合根按同源规则提供）。
type Store struct {
	db       *sql.DB
	postgres bool

	mu      sync.Mutex
	ensured bool
}

// OpenStore builds the queue store over an existing handle; the caller owns
// handle lifecycle（组合根的 SQLite 连接 / PG pool 统一关闭）。
func OpenStore(db *sql.DB, postgres bool) (*Store, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return &Store{db: db, postgres: postgres}, nil
}

// Table 返回方言限定的表名（PG 走 juhe_dataset schema，SQLite 单库直名）。
func (s *Store) Table() string {
	if s.postgres {
		return PGSchema + "." + TableName
	}
	return TableName
}

// bind 把 ? 占位符改写为 PG 的 $N（gateway DurableDispatch.bind 同语义）。
func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
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
	if _, err := s.db.ExecContext(ctx, s.bind(schema)); err != nil {
		return fmt.Errorf("初始化 %s schema 失败: %w", s.Table(), err)
	}
	if err := s.ensureSnapshotColumns(ctx); err != nil {
		return fmt.Errorf("升级 %s schema 失败: %w", s.Table(), err)
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
		query := "ALTER TABLE " + s.Table() + " ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"
		if s.postgres {
			query = "ALTER TABLE " + s.Table() + " ADD COLUMN IF NOT EXISTS " + column + " TEXT NOT NULL DEFAULT ''"
		}
		if _, err := s.db.ExecContext(ctx, s.bind(query)); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}

// existingColumns 按方言列出既有列名（SQLite PRAGMA table_info 的行形状是
// cid/name/type/notnull/dflt_value/pk，取 name；PG 走 information_schema）。
func (s *Store) existingColumns(ctx context.Context) (map[string]bool, error) {
	if !s.postgres {
		rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+TableName+`)`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		columns := map[string]bool{}
		for rows.Next() {
			var (
				cid, notnull, pk int
				name, columnType string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &columnType, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			columns[name] = true
		}
		return columns, rows.Err()
	}
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

// Dequeue 按 drain 契约读取队头批次：ORDER BY created_at ASC（gateway 写入
// 的 created_at 是规范化 UTC ISO 文本，字典序即时间序），id 作确定性
// tiebreaker（与其他清理查询 ORDER BY time, id 先例一致）。读取不锁行：
// 单 jobs 进程消费语义与 Node worker 本地队列一致，行在执行成功后才删除。
// v2 列集一并读出：快照行的 account_id/kind/source/snapshot_json/updated_at
// 映射为 retention.RecordMaintenanceJob 的执行器输入（snapshot_json 反序列
// 化为对象；清理行这些列为 ” / 0 哨兵，对应字段留零值）。snapshot_json
// 损坏按失败返回，行保留队头等待重试（不静默丢弃）。
func (s *Store) Dequeue(ctx context.Context, limit int) ([]retention.RecordMaintenanceJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, type, cutoff_at, batch_size, max_batches, created_at,
	account_id, kind, source, snapshot_json, updated_at
FROM `+s.Table()+` ORDER BY created_at ASC, id ASC LIMIT ?`), limit)
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
				return nil, fmt.Errorf("数据维护任务 %s 的 snapshot_json 无效: %w", job.ID, err)
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
