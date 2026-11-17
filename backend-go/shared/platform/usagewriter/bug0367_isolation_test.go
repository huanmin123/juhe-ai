package usagewriter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BUG-0367 根治回归：写计划期非法记录按记录隔离，死信落独立终态目录，
// 绝不回灌溢出回放目录（否则形成「死信→回放→再失败」无限环）。

func TestBuildWritePlanIsolatedRejectsInvalidRecord(t *testing.T) {
	valid := gatewayInput("isolated-valid")
	invalid := gatewayInput("isolated-invalid")
	invalid.SystemAccountID = ""
	badSource := gatewayInput("isolated-bad-source")
	badSource.TrafficSource = "unknown-source"

	plan, rejects, err := BuildWritePlanIsolated(context.Background(),
		[]UsageRecordInput{valid, invalid, badSource},
		WritePlanOptions{ShardCount: 16},
		fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatalf("非记录级故障不应返回 error: %v", err)
	}
	if got := writePlanRowCount(plan); got != 1 {
		t.Fatalf("合法记录必须保留在计划中: rows=%d", got)
	}
	if len(rejects) != 2 {
		t.Fatalf("两条非法记录必须进入 rejects: %+v", rejects)
	}
	for _, rejected := range rejects {
		if rejected.Reason == "" {
			t.Fatalf("reject 必须带原因: %+v", rejected)
		}
	}
}

func TestWriterIsolatesInvalidRecordAndKeepsValidOnes(t *testing.T) {
	store := &mockStore{}
	overflow := NewFileOverflowSpool(t.TempDir())
	deadLetter := NewFileDeadLetterSpool(t.TempDir())
	writer, _ := newTestWriter(t, Config{
		BatchSize: 2, FlushIntervalMs: 5_000, MaxWriteAttempts: 5,
	}, store, WithOverflowSpool(overflow), WithDeadLetterSpool(deadLetter))

	invalid := gatewayInput("isolate-invalid")
	invalid.SystemAccountID = ""
	if err := writer.Enqueue(context.Background(), invalid); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), gatewayInput("isolate-valid")); err != nil {
		t.Fatal(err)
	}

	eventually(2*time.Second, func() bool {
		return writer.Runtime().DeadLetterCount == 1 && store.totalRows() == 1 && writer.PendingCount() == 0
	})
	if got := store.totalRows(); got != 1 {
		t.Fatalf("同批合法记录必须照常写库: rows=%d", got)
	}
	if got := writer.Runtime().FlushFailureCount; got != 0 {
		t.Fatalf("按记录隔离不得记整批失败: flushFailureCount=%d", got)
	}
	dead := readDeadLetterRecords(t, deadLetter, "jobs")
	if len(dead) != 1 || dead[0].TraceID != "isolate-invalid" {
		t.Fatalf("非法记录必须进入死信终态目录: %+v", dead)
	}
	if files := listJSONFiles(t, overflow.Directory, "jobs"); len(files) != 0 {
		t.Fatalf("死信不得落入溢出回放目录: %v", files)
	}
}

func TestWriterDeadLetterExhaustedRetryGoesToTerminalDirectory(t *testing.T) {
	store := &mockStore{failures: 1 << 20, overflowErr: errors.New("db down")}
	overflow := NewFileOverflowSpool(t.TempDir())
	deadLetter := NewFileDeadLetterSpool(t.TempDir())
	writer, _ := newTestWriter(t, Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 2,
	}, store, WithOverflowSpool(overflow), WithDeadLetterSpool(deadLetter))

	if err := writer.Enqueue(context.Background(), gatewayInput("exhausted")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount == 1 })
	if writer.PendingCount() != 0 {
		t.Fatalf("死信终态后必须出队: pending=%d", writer.PendingCount())
	}
	if dead := readDeadLetterRecords(t, deadLetter, "jobs"); len(dead) != 1 {
		t.Fatalf("重试耗尽批次必须落死信终态目录: %+v", dead)
	}
	if files := listJSONFiles(t, overflow.Directory, "jobs"); len(files) != 0 {
		t.Fatalf("死信不得落入溢出回放目录（会被 Drainer 回放）: %v", files)
	}
}

func TestFileDeadLetterSpoolIsIdempotent(t *testing.T) {
	spool := NewFileDeadLetterSpool(t.TempDir())
	input := gatewayInput("idempotent-dead-letter")
	input.ID = "usage_dup_0001"
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if files := listJSONFiles(t, spool.Directory, "jobs"); len(files) != 1 {
		t.Fatalf("同一 usageRecordId 重复落盘必须覆盖同名文件: %v", files)
	}
}

func readDeadLetterRecords(t *testing.T, spool *FileDeadLetterSpool, instance string) []UsageRecordInput {
	t.Helper()
	directory := filepath.Join(spool.Directory, instance)
	files := listJSONFiles(t, spool.Directory, instance)
	records := make([]UsageRecordInput, 0, len(files))
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("读取死信文件失败: %v", err)
		}
		var record UsageRecordInput
		if err := json.Unmarshal(content, &record); err != nil {
			t.Fatalf("死信文件必须是完整 UsageRecordInput JSON: %v", err)
		}
		records = append(records, record)
	}
	return records
}

// listJSONFiles 列出 spool 根目录下实例子目录里的记录文件（不存在视为空）。
func listJSONFiles(t *testing.T, root, instance string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, instance))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("读取 spool 目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}
