package recordmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// ---- OpenStore 与列探测 ----

func TestOpenStoreValidation(t *testing.T) {
	if _, err := OpenStore(nil, false); err == nil {
		t.Fatalf("nil 数据库应报错")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenStore(db, false)
	if err != nil || store == nil {
		t.Fatalf("合法构造不应报错: %v", err)
	}
}

func TestExistingColumnsSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 表不存在：返回空列集而不报错。
	columns, err := store.existingColumns(ctx)
	if err != nil || len(columns) != 0 {
		t.Fatalf("缺表应返回空列集: %#v %v", columns, err)
	}
	if _, err := db.Exec(`CREATE TABLE ` + TableName + ` (id TEXT PRIMARY KEY, kind TEXT, snapshot_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	columns, err = store.existingColumns(ctx)
	if err != nil {
		t.Fatalf("读取列失败: %v", err)
	}
	for _, name := range []string{"id", "kind", "snapshot_json"} {
		if !columns[name] {
			t.Fatalf("列 %s 应存在: %#v", name, columns)
		}
	}
}

func TestIsDuplicateColumnErrorVariants(t *testing.T) {
	if isDuplicateColumnError(nil) {
		t.Fatalf("nil 不应匹配")
	}
	if isDuplicateColumnError(errors.New("some other failure")) {
		t.Fatalf("其他错误不应匹配")
	}
	if !isDuplicateColumnError(errors.New("Duplicate Column name: x")) {
		t.Fatalf("大小写不敏感匹配 duplicate column")
	}
}

// ---- Drainer 配置回退与快照批量降级 ----

func TestDrainerConfigFallbacks(t *testing.T) {
	drainer := &Drainer{}
	if drainer.flushInterval() != DefaultFlushIntervalMs*time.Millisecond {
		t.Fatalf("默认 flush 间隔不符: %v", drainer.flushInterval())
	}
	if drainer.retryDelay() != DefaultRetryDelay {
		t.Fatalf("默认重试延迟不符: %v", drainer.retryDelay())
	}
	if drainer.batchSize() != DefaultBatchSize {
		t.Fatalf("默认批次大小不符: %d", drainer.batchSize())
	}
	if drainer.logger() == nil {
		t.Fatalf("默认 logger 应被填充")
	}
	custom := &Drainer{BatchSize: 7, FlushInterval: time.Second, RetryDelay: 2 * time.Second}
	if custom.batchSize() != 7 || custom.flushInterval() != time.Second || custom.retryDelay() != 2*time.Second {
		t.Fatalf("自定义配置应生效")
	}
}

type woPlainRunner struct {
	runs []retention.RecordMaintenanceJob
	err  error
}

func (r *woPlainRunner) RunOnce(_ context.Context, job retention.RecordMaintenanceJob) (map[string]any, error) {
	r.runs = append(r.runs, job)
	return nil, r.err
}

type woBatchRunner struct {
	woPlainRunner
	batches int
	err     error
}

func (r *woBatchRunner) RunAccountUsageSnapshotUpserts(_ context.Context, jobs []retention.RecordMaintenanceJob) (map[string]any, error) {
	r.batches++
	r.runs = append(r.runs, jobs...)
	return nil, r.err
}

func TestRunSnapshotUpsertRunBatchAndFallback(t *testing.T) {
	ctx := context.Background()
	jobs := []retention.RecordMaintenanceJob{{Type: "account_usage_snapshot_upsert"}, {Type: "account_usage_snapshot_upsert"}}
	// 未实现批量接口：逐行执行。
	plain := &woPlainRunner{}
	drainer := &Drainer{Runner: plain}
	if err := drainer.runSnapshotUpsertRun(ctx, jobs); err != nil {
		t.Fatalf("逐行执行不应报错: %v", err)
	}
	if len(plain.runs) != len(jobs) {
		t.Fatalf("逐行应执行 %d 次: %d", len(jobs), len(plain.runs))
	}
	// 实现批量接口：合并为一次往返。
	batch := &woBatchRunner{}
	batchDrainer := &Drainer{Runner: batch}
	if err := batchDrainer.runSnapshotUpsertRun(ctx, jobs); err != nil {
		t.Fatalf("批量执行不应报错: %v", err)
	}
	if batch.batches != 1 || len(batch.runs) != len(jobs) {
		t.Fatalf("批量应一次往返处理全部任务: %d %d", batch.batches, len(batch.runs))
	}
	// 批量失败错误上抛。
	failing := &woBatchRunner{err: errors.New("batch failed")}
	failingDrainer := &Drainer{Runner: failing}
	if err := failingDrainer.runSnapshotUpsertRun(ctx, jobs); err == nil || !strings.Contains(err.Error(), "batch failed") {
		t.Fatalf("批量失败应上抛: %v", err)
	}
	// 逐行失败错误上抛。
	failingPlain := &woPlainRunner{err: errors.New("row failed")}
	rowDrainer := &Drainer{Runner: failingPlain}
	if err := rowDrainer.runSnapshotUpsertRun(ctx, jobs[:1]); err == nil || !strings.Contains(err.Error(), "row failed") {
		t.Fatalf("逐行失败应上抛: %v", err)
	}
}
