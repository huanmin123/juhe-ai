package usagespooldrain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"

	_ "modernc.org/sqlite"
)

// gatewaySpoolFile 复现 gateway spool Persist 的写出格式（单文件单记录，
// json.Marshal + '\n'）。usagewriter.UsageRecordInput 与
// gateway/internal/gatewayusage UsageRecordInput 的 JSON 标签逐字段一致
// （两侧 records.go 冻结合同），同一值序列化字节相同。
func gatewaySpoolFile(t *testing.T, directory string, token string, record usagewriter.UsageRecordInput) string {
	t.Helper()
	instanceDirectory := filepath.Join(directory, "gateway-chain")
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(instanceDirectory, token+".json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseRecord(id string) usagewriter.UsageRecordInput {
	return usagewriter.UsageRecordInput{
		ID:              id,
		TraceID:         "trace-" + id,
		TrafficSource:   usagewriter.TrafficSourceGateway,
		SystemAccountID: "sys1",
		Success:         true,
		CreatedAt:       "2026-01-02T03:04:05.000Z",
	}
}

// silentLogger 吞掉 drain 事件日志，保持测试输出干净。
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingEnqueuer 记录入队调用，可注入失败。
type recordingEnqueuer struct {
	inputs []usagewriter.UsageRecordInput
	failOn map[string]error
}

func (e *recordingEnqueuer) Enqueue(ctx usagewriter.Ctx, input usagewriter.UsageRecordInput) error {
	if err, ok := e.failOn[input.ID]; ok {
		return err
	}
	e.inputs = append(e.inputs, input)
	return nil
}

func newTestDrainer(directory string, enqueuer Enqueuer) *Drainer {
	return &Drainer{
		Directory: directory,
		Enqueuer:  enqueuer,
		Logger:    silentLogger(),
	}
}

func TestDrainOnceEmptyAndMissingDirectory(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	missing := newTestDrainer(filepath.Join(t.TempDir(), "not-created"), enqueuer)
	processed, err := missing.DrainOnce(context.Background())
	if err != nil || processed != 0 {
		t.Fatalf("missing directory: processed=%d err=%v", processed, err)
	}
	empty := newTestDrainer(t.TempDir(), enqueuer)
	processed, err = empty.DrainOnce(context.Background())
	if err != nil || processed != 0 {
		t.Fatalf("empty directory: processed=%d err=%v", processed, err)
	}
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("enqueued = %d", len(enqueuer.inputs))
	}
}

func TestDrainOnceQuarantinesCorruptFiles(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &recordingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)

	good := gatewaySpoolFile(t, directory, "0001-good", baseRecord("id-good"))
	cases := []struct {
		token   string
		content string
		reason  string
	}{
		{"0002-badjson", "not-json\n", "格式错误"},
		{"0003-noid", `{"traceId":"t","createdAt":"2026-01-02T03:04:05.000Z"}` + "\n", "缺少稳定 id"},
		{"0004-nocreatedat", `{"id":"x","traceId":"t"}` + "\n", "缺少稳定 createdAt"},
		{"0005-badcreatedat", `{"id":"x","createdAt":"not-a-time"}` + "\n", "createdAt 非法"},
	}
	for _, testCase := range cases {
		instanceDirectory := filepath.Join(directory, "gateway-chain")
		if err := os.WriteFile(filepath.Join(instanceDirectory, testCase.token+".json"), []byte(testCase.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	processed, err := drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1（仅合法文件）", processed)
	}
	if _, err := os.Stat(good); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("合法文件未被删除: %v", err)
	}
	for _, testCase := range cases {
		corruptPath := filepath.Join(directory, "gateway-chain", testCase.token+".json.corrupt")
		if _, err := os.Stat(corruptPath); err != nil {
			t.Fatalf("%s 未隔离为 .corrupt: %v", testCase.reason, err)
		}
		original := corruptPath[:len(corruptPath)-len(".corrupt")]
		if _, err := os.Stat(original); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s 原文件仍在: %v", testCase.token, err)
		}
	}
	if len(enqueuer.inputs) != 1 || enqueuer.inputs[0].ID != "id-good" {
		t.Fatalf("enqueued = %+v", enqueuer.inputs)
	}
}

func TestDrainOnceKeepsFileWhenEnqueueFails(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &recordingEnqueuer{failOn: map[string]error{"id-fail": errors.New("usage writer 已停止，拒绝写入使用记录")}}
	drainer := newTestDrainer(directory, enqueuer)

	failing := gatewaySpoolFile(t, directory, "0001-fail", baseRecord("id-fail"))
	// 排在失败文件之后的文件不得被处理（head-of-line，失败即停本轮）。
	after := gatewaySpoolFile(t, directory, "0002-after", baseRecord("id-after"))

	processed, err := drainer.DrainOnce(context.Background())
	if err == nil {
		t.Fatal("期望入队失败返回错误")
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, err := os.Stat(failing); err != nil {
		t.Fatalf("失败文件被删除: %v", err)
	}
	if _, err := os.Stat(after); err != nil {
		t.Fatalf("失败文件之后的文件被提前消费: %v", err)
	}

	// 失败恢复后同一文件重试成功。
	delete(enqueuer.failOn, "id-fail")
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("恢复后 processed = %d, want 2", processed)
	}
	if _, err := os.Stat(failing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("重试成功后文件未删除: %v", err)
	}
	if _, err := os.Stat(after); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("后续文件未删除: %v", err)
	}
	if got := len(enqueuer.inputs); got != 2 {
		t.Fatalf("enqueued = %d, want 2", got)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &recordingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	drainer.FlushInterval = 10 * time.Millisecond

	gatewaySpoolFile(t, directory, "0001-run", baseRecord("id-run"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		drainer.Run(ctx)
		close(done)
	}()
	deadline := time.After(3 * time.Second)
	for len(enqueuer.inputs) == 0 {
		select {
		case <-done:
			t.Fatal("Run 在取消前提前退出")
		case <-deadline:
			t.Fatal("等待入队超时")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if _, err := os.Stat(filepath.Join(directory, "gateway-chain", "0001-run.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("停机排空未消费文件: %v", err)
	}
}

// TestDrainIntoWriterEndToEnd 是组合根闭环（gateway 形状 spool 文件 →
// drain → usagewriter 分片落库 → 业务库副作用 → 幂等重放不产生双行），
// 全部限制在 t.TempDir() 内。
func TestDrainIntoWriterEndToEnd(t *testing.T) {
	root := t.TempDir()
	spoolDirectory := filepath.Join(root, "usage-record-spool")
	shardRoot := filepath.Join(root, "usage-shards")

	catalog, err := sql.Open("sqlite", "file:"+filepath.Join(root, "catalog.sqlite3")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	business, err := sql.Open("sqlite", "file:"+filepath.Join(root, "business.sqlite3")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	if _, err := business.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, last_used_at TEXT, updated_at TEXT, deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`INSERT INTO accounts (id) VALUES ('acc1')`); err != nil {
		t.Fatal(err)
	}

	store := usagewriter.NewSqliteShardStore(usagewriter.SqliteShardStoreConfig{
		CatalogDB:  catalog,
		ShardRoot:  shardRoot,
		ShardCount: 4,
		BusinessDB: business,
	})
	if err := store.EnsureCatalogSchema(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// FreezePricing/CatalogSnapshot 与组合根装配一致（writer 入队时点冻结
	// 定价事实；catalog port 未在 jobs 适配，回落确定性 fallback 快照）。
	writer := usagewriter.NewWriter(usagewriter.Config{
		BatchSize:          8,
		FlushBatchMaxBytes: 8 * 1024 * 1024,
		ShardCount:         4,
		ShardRoot:          shardRoot,
		FreezePricing:      true,
		CatalogSnapshot:    true,
	}, store, nil)
	drainer := newTestDrainer(spoolDirectory, writer)

	// gateway 形状记录（含完整 account scope + token 维度，触发冻结路径与
	// last_used_at 副作用）。
	record := baseRecord("usage_20260102_s00_1767225600000_e1")
	record.AccountID = "acc1"
	record.AccountOwnerSystemAccountID = "sys1"
	record.AccountAccessType = usagewriter.AccountAccessTypeOwner
	record.Model = "gpt-5"
	record.InputTokens = intPtr(5)
	record.CostUsd = floatPtr(0.25)
	gatewaySpoolFile(t, spoolDirectory, "1767225600000-1000-00000000-0000-4000-8000-000000000001", record)

	processed, err := drainer.DrainOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("first drain: processed=%d err=%v", processed, err)
	}
	writer.Drain()

	if got := writer.Runtime().WrittenRecords; got != 1 {
		t.Fatalf("written = %d, want 1", got)
	}
	shardRows := countShardRows(t, shardRoot, "trace-"+record.ID)
	if shardRows != 1 {
		t.Fatalf("usage rows = %d, want 1", shardRows)
	}
	var lastUsed sql.NullString
	if err := business.QueryRow(`SELECT last_used_at FROM accounts WHERE id = 'acc1'`).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if !lastUsed.Valid || lastUsed.String != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("last_used_at = %v", lastUsed)
	}

	// 幂等重放：同一记录再次进入 spool（重复投递），分片仍只有一行。
	gatewaySpoolFile(t, spoolDirectory, "1767225600000-1000-00000000-0000-4000-8000-000000000002", record)
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("replay drain: processed=%d err=%v", processed, err)
	}
	writer.Drain()
	if shardRows := countShardRows(t, shardRoot, "trace-"+record.ID); shardRows != 1 {
		t.Fatalf("usage rows after replay = %d, want 1", shardRows)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer.Close(stopCtx)
}

func countShardRows(t *testing.T, shardRoot string, traceID string) int {
	t.Helper()
	total := 0
	err := filepath.Walk(shardRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".sqlite3") {
			return nil
		}
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			return err
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE trace_id = ?`, traceID).Scan(&count); err != nil {
			return err
		}
		total += count
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func intPtr(value int) *int           { return &value }
func floatPtr(value float64) *float64 { return &value }
