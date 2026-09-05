// Package recordmaintenance 消费 gateway cleanup POST 写入的
// record_maintenance_jobs 持久交接表。表结构与写入语义的契约在 gateway
// internal/tablemonitor/enqueue.go 包文档：列即 Node 规范化
// non_business_data_cleanup job 字段（id/type/cutoff_at/batch_size/
// max_batches/created_at），gateway 侧 SQLite 落业务库文件、PostgreSQL 落
// juhe_dataset schema。
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
  created_at TEXT NOT NULL
)`

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

// EnsureSchema 运行时建表（幂等；gateway 侧同样按需建表，先到先建）。
func (s *Store) EnsureSchema(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensured {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, s.bind(schema)); err != nil {
		return fmt.Errorf("初始化 %s schema 失败: %w", s.Table(), err)
	}
	s.ensured = true
	return nil
}

// Dequeue 按 drain 契约读取队头批次：ORDER BY created_at ASC（gateway 写入
// 的 created_at 是规范化 UTC ISO 文本，字典序即时间序），id 作确定性
// tiebreaker（与其他清理查询 ORDER BY time, id 先例一致）。读取不锁行：
// 单 jobs 进程消费语义与 Node worker 本地队列一致，行在执行成功后才删除。
func (s *Store) Dequeue(ctx context.Context, limit int) ([]retention.RecordMaintenanceJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, type, cutoff_at, batch_size, max_batches, created_at
FROM `+s.Table()+` ORDER BY created_at ASC, id ASC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]retention.RecordMaintenanceJob, 0, limit)
	for rows.Next() {
		var job retention.RecordMaintenanceJob
		if err := rows.Scan(&job.ID, &job.Type, &job.CutoffAt, &job.BatchSize, &job.MaxBatches, &job.CreatedAt); err != nil {
			return nil, err
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
