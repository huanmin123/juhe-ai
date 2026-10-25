// Package recordmaintenance owns the gateway side of the durable
// record_maintenance_jobs handover table (gateway writes, jobs polls).
//
// 归档 Node 入队机制（record-maintenance-queue.service.ts
// enqueueRecordMaintenanceJobWithResultAsync）：server 角色经
// sendRecordMaintenanceJobsToWorker IPC 投递 background worker（Redis Stream /
// db-service process.send 是另外两个角色形态），返回
// { job, queued, droppedReason }。
//
// Go 等价选型：Redis Stream 与跨进程 IPC 按 Go 总设计消灭（jobs registry
// background_worker_record_maintenance 条目：组合根本地队列 flush 循环）；gateway
// 与 jobs 不同 module，既有跨进程通道是「持久化任务表 + jobs 轮询」
// （api_key_record_cleanup_targets / account_test_tasks 先例）。因此 gateway
// 侧按 Node RecordMaintenanceJob 形状写入 juhe_dataset schema 的
// record_maintenance_jobs 表。jobs 侧 drain 契约（jobs wave 接线，Node 本地队列 flush
// 语义）：ORDER BY created_at 读取、RunOnce 成功后按 id 删除、失败保留等待重试。
//
// 交接契约 v2（codex usage headers 持久化扩展载荷）：表承载
// account_usage_snapshot_upsert 行（Node record-maintenance-queue.service.ts
// RecordMaintenanceJob union 的该变体，payload = accountId/kind/source/
// snapshot/updatedAt + 公共 id/createdAt）与历史 non_business_data_cleanup 行
// （生产者面已随表监控路由族删除；jobs drain 仍消费存量清理行）。
// 快照 5 列与既有清理列互斥填充：清理行写 cutoff_at/batch_size/max_batches，
// 快照行写 account_id/kind/source/snapshot_json/updated_at（JSON 文本），
// 其余列写 ” / 0 哨兵。列集与 jobs internal/recordmaintenance/queue.go 的
// schema 逐字一致；对既有 6 列旧表按列名补 ALTER ADD COLUMN（幂等，见
// ensureSnapshotColumns），DDL 双侧先到先建、各侧自升级。
package recordmaintenance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RecordMaintenanceJobTypeNonBusinessDataCleanup 与 Node job.type 一致
// （存量清理行的表内家族；gateway 生产者面已随表监控路由族删除）。
const RecordMaintenanceJobTypeNonBusinessDataCleanup = "non_business_data_cleanup"

// RecordMaintenanceJobTypeAccountUsageSnapshotUpsert 与 Node job.type 一致。
const RecordMaintenanceJobTypeAccountUsageSnapshotUpsert = "account_usage_snapshot_upsert"

// recordMaintenanceTableName 是 gateway 写入、jobs 消费的交接表。
const recordMaintenanceTableName = "record_maintenance_jobs"

// recordMaintenancePGSchema 是 PostgreSQL 方言下交接表所在 schema（jobs 侧
// internal/recordmaintenance/queue.go 的 PGSchema 同值；两项目各自持有常量，
// 改动需双端同步）。
const recordMaintenancePGSchema = "juhe_dataset"

// recordMaintenanceSchema 运行时建表（accounttesttask.Schema 先例：Go-owned
// 交接关系不占用生产 migration catalog）。列即 Node RecordMaintenanceJob
// union 的 non_business_data_cleanup + account_usage_snapshot_upsert 两个
// 变体的规范化字段并集；与 jobs internal/recordmaintenance/queue.go schema
// 逐字一致。表名带 juhe_dataset 限定：DDL 与读写路径必须同一 schema，不能
// 依赖连接 search_path（否则会在 public 建表而 INSERT/查询不到，W32 同源
// 缺陷）。
var recordMaintenanceSchema = `CREATE TABLE IF NOT EXISTS juhe_dataset.record_maintenance_jobs (
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

// recordMaintenanceSnapshotColumns 是 v2 契约对既有 6 列旧表的加法式升级列
// （account_usage_snapshot_upsert 载荷；顺序即建表 DDL 中的列序）。
var recordMaintenanceSnapshotColumns = []string{"account_id", "kind", "source", "snapshot_json", "updated_at"}

// DispatchResult mirrors RecordMaintenanceEnqueueResult: queued=false keeps
// the Node droppedReason vocabulary (worker_dispatch_failed = the durable
// channel rejected the job).
type DispatchResult struct {
	Queued        bool
	DroppedReason string
}

// DurableDispatch persists the Node-shaped job row; a nil-safe default at
// composition time, mockable in tests.
type DurableDispatch struct {
	db  *sql.DB
	now func() time.Time

	mu        sync.Mutex
	ensured   bool
	ensureErr error
}

// NewDurableRecordMaintenanceDispatch builds the durable dispatch; now may be
// nil for the process clock.
func NewDurableRecordMaintenanceDispatch(db *sql.DB, now func() time.Time) (*DurableDispatch, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	if now == nil {
		now = time.Now
	}
	return &DurableDispatch{db: db, now: now}, nil
}

func (d *DurableDispatch) table() string {
	return recordMaintenancePGSchema + "." + recordMaintenanceTableName
}

func (d *DurableDispatch) bind(query string) string {
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

// ensureSchema 幂等建表 + 旧表加法式升级：CREATE IF NOT EXISTS 后按列名补齐
// v2 快照列。先查列再 ALTER；并发双写方（gateway/jobs 两侧各自 ensure）同时
// 命中同一缺失列时，后到方以 duplicate column 容错收敛。
func (d *DurableDispatch) ensureSchema(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ensured {
		return d.ensureErr
	}
	if _, err := d.db.ExecContext(ctx, d.bind(recordMaintenanceSchema)); err != nil {
		d.ensureErr = err
		return d.ensureErr
	}
	existing, err := d.existingColumns(ctx)
	if err != nil {
		d.ensureErr = err
		return d.ensureErr
	}
	for _, column := range recordMaintenanceSnapshotColumns {
		if existing[column] {
			continue
		}
		if _, err := d.db.ExecContext(ctx, d.bind("ALTER TABLE "+d.table()+
			" ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''")); err != nil && !isDuplicateColumnError(err) {
			d.ensureErr = err
			return d.ensureErr
		}
	}
	d.ensured = true
	return nil
}

// existingColumns 经 information_schema 列出既有列名（juhe_dataset schema）。
func (d *DurableDispatch) existingColumns(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, d.bind(
		`SELECT column_name FROM information_schema.columns WHERE table_schema = ? AND table_name = ?`),
		recordMaintenancePGSchema, recordMaintenanceTableName)
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

// isDuplicateColumnError 容错并发双写方同时补列的竞态。
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

// RecordMaintenanceSnapshotJob mirrors the normalized Node
// account_usage_snapshot_upsert job payload（gatewaycodex usage headers
// persist 的产出形状）；id/createdAt/updatedAt 由 enqueue 按
// normalizeRecordMaintenanceJob 语义填充/规范化。
type RecordMaintenanceSnapshotJob struct {
	AccountID string
	Kind      string
	Source    string
	Snapshot  map[string]any
	UpdatedAt string
}

// EnqueueAccountUsageSnapshotUpsert writes the account_usage_snapshot_upsert
// job row（Node enqueueRecordMaintenanceJob 对该 job 的规范化 + 投递等价）。
// updatedAt 规范化为 UTC ISO（jobs 执行器会再次校验，无效值在通道口拒绝，
// 避免毒化队头）；快照序列化失败同样回 worker_dispatch_failed，调用面
// 保持 fire-and-forget 契约。
func (d *DurableDispatch) EnqueueAccountUsageSnapshotUpsert(ctx context.Context, job RecordMaintenanceSnapshotJob) DispatchResult {
	updatedAt, ok := canonicalInstant(job.UpdatedAt)
	if !ok {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	snapshotJSON, err := json.Marshal(job.Snapshot)
	if err != nil {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	if err := d.ensureSchema(ctx); err != nil {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	now := d.now()
	_, err = d.db.ExecContext(ctx, d.bind(`INSERT INTO `+d.table()+`
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		newRecordMaintenanceJobID(now), RecordMaintenanceJobTypeAccountUsageSnapshotUpsert, "",
		0, 0, now.UTC().Format("2006-01-02T15:04:05.000Z"),
		job.AccountID, job.Kind, job.Source, string(snapshotJSON), updatedAt)
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

// rfc3339Pattern bounds canonicalInstant to the Node instant shape.
var rfc3339Pattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// canonicalInstant mirrors canonicalizeRfc3339Instant: UTC ISO with
// millisecond precision (Node toISOString).
func canonicalInstant(value string) (string, bool) {
	millis, ok := instantMillis(value)
	if !ok {
		return "", false
	}
	return time.UnixMilli(millis).UTC().Format("2006-01-02T15:04:05.000Z"), true
}

func instantMillis(value string) (int64, bool) {
	if !rfc3339Pattern.MatchString(strings.TrimSpace(value)) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}
