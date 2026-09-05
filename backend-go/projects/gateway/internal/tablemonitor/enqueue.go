// Durable record-maintenance dispatch for the cleanup POST.
//
// 归档 Node 入队机制（modules/table-monitor/table-monitor.routes.ts →
// record-maintenance-queue.service.ts enqueueRecordMaintenanceJobWithResultAsync）：
// normalizeRecordMaintenanceJob（id=newId('recmaint')、createdAt=nowIso、cutoffAt
// 规范化 UTC ISO）→ server 角色经 sendRecordMaintenanceJobsToWorker IPC 投递
// background worker（Redis Stream / db-service process.send 是另外两个角色形态），
// 返回 { job, queued, droppedReason }。
//
// Go 等价选型：Redis Stream 与跨进程 IPC 按 Go 总设计消灭（jobs registry
// background_worker_record_maintenance 条目：组合根本地队列 flush 循环）；gateway
// 与 jobs 不同 module，既有跨进程通道是「持久化任务表 + jobs 轮询」
// （api_key_record_cleanup_targets / account_test_tasks 先例）。因此 gateway 侧按
// Node RecordMaintenanceJob 形状写入 record_maintenance_jobs 表：SQLite 落业务库
// 文件（与 api_key_record_cleanup_targets 同放置约定），PostgreSQL 落
// juhe_dataset schema。jobs 侧 drain 契约（jobs wave 接线，Node 本地队列 flush
// 语义）：ORDER BY created_at 读取、RunOnce 成功后按 id 删除、失败保留等待重试。
package tablemonitor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RecordMaintenanceJobTypeNonBusinessDataCleanup 与 Node job.type 一致。
const RecordMaintenanceJobTypeNonBusinessDataCleanup = "non_business_data_cleanup"

// recordMaintenanceTableName 是 gateway 写入、jobs 消费的交接表。
const recordMaintenanceTableName = "record_maintenance_jobs"

// recordMaintenanceSchema 运行时建表（accounttesttask.Schema 先例：Go-owned
// 交接关系不占用生产 migration catalog）。列即 Node
// non_business_data_cleanup job 的规范化字段。
var recordMaintenanceSchema = `CREATE TABLE IF NOT EXISTS record_maintenance_jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  cutoff_at TEXT NOT NULL,
  batch_size INTEGER NOT NULL,
  max_batches INTEGER NOT NULL,
  created_at TEXT NOT NULL
)`

// RecordMaintenanceJob mirrors the normalized Node non_business_data_cleanup
// job whose id/createdAt the receipt echoes.
type RecordMaintenanceJob struct {
	ID         string
	CutoffAt   string
	BatchSize  int
	MaxBatches int
	CreatedAt  string
}

// DispatchResult mirrors RecordMaintenanceEnqueueResult: queued=false keeps
// the Node droppedReason vocabulary (worker_dispatch_failed = the durable
// channel rejected the job).
type DispatchResult struct {
	Queued        bool
	DroppedReason string
}

// RecordMaintenanceDispatch is the cleanup enqueue port (Node
// enqueueRecordMaintenanceJobWithResultAsync at the route boundary).
type RecordMaintenanceDispatch interface {
	EnqueueNonBusinessDataCleanup(ctx context.Context, job RecordMaintenanceJob) DispatchResult
}

// DurableDispatch persists the Node-shaped job row; a nil-safe default at
// composition time, mockable in tests.
type DurableDispatch struct {
	db  *sql.DB
	pg  bool
	now func() time.Time

	mu        sync.Mutex
	ensured   bool
	ensureErr error
}

// NewDurableRecordMaintenanceDispatch builds the durable dispatch; now may be
// nil for the process clock.
func NewDurableRecordMaintenanceDispatch(db *sql.DB, pgDialect bool, now func() time.Time) (*DurableDispatch, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if now == nil {
		now = time.Now
	}
	return &DurableDispatch{db: db, pg: pgDialect, now: now}, nil
}

func (d *DurableDispatch) table() string {
	if d.pg {
		return "juhe_dataset." + recordMaintenanceTableName
	}
	return recordMaintenanceTableName
}

func (d *DurableDispatch) bind(query string) string {
	if !d.pg {
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

// EnqueueNonBusinessDataCleanup writes the job row (worker-dispatch
// equivalent). Schema creation failures and insert failures surface as the
// Node worker_dispatch_failed receipt so the route keeps its 200-receipt
// contract (Node IPC-unavailable semantics: the dispatch failure never 500s).
func (d *DurableDispatch) EnqueueNonBusinessDataCleanup(ctx context.Context, job RecordMaintenanceJob) DispatchResult {
	d.mu.Lock()
	if !d.ensured {
		_, d.ensureErr = d.db.ExecContext(ctx, d.bind(recordMaintenanceSchema))
		if d.ensureErr == nil {
			d.ensured = true
		}
	}
	ensureErr := d.ensureErr
	d.mu.Unlock()
	if ensureErr != nil {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	_, err := d.db.ExecContext(ctx, d.bind(`INSERT INTO `+d.table()+`
		(id, type, cutoff_at, batch_size, max_batches, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`),
		job.ID, RecordMaintenanceJobTypeNonBusinessDataCleanup, job.CutoffAt,
		job.BatchSize, job.MaxBatches, job.CreatedAt)
	if err != nil {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	return DispatchResult{Queued: true}
}

// newRecordMaintenanceJobID mirrors newId('recmaint'):
// recmaint_<epochMillis>_<8 hex chars>.
func newRecordMaintenanceJobID(now time.Time) string {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("recmaint_%d", now.UnixMilli())
	}
	return fmt.Sprintf("recmaint_%d_%s", now.UnixMilli(), hex.EncodeToString(random[:]))
}
