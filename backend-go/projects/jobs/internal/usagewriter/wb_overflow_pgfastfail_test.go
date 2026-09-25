package usagewriter

// 统计链路排查修复的回归测试：溢出 spool 文件补偿（B②）与 PG 写计划期
// 快速失败（B③）。文件名前缀 wb 标记本批次修复。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFileOverflowSpoolWritesReplayableRecord 锁定溢出文件格式与 gateway
// usage spool 同构：单文件单记录 json+\\n、稳定 id/createdAt、无 .tmp 残留，
// usagespooldrain.parseSpoolRecord 的解析语义（json.Unmarshal +
// NormalizeUsageRecordInput）可原样回放。
func TestFileOverflowSpoolWritesReplayableRecord(t *testing.T) {
	directory := t.TempDir()
	spool := NewFileOverflowSpool(directory)
	input := gatewayInput("overflow-1")
	input.ID = "usage_20260102_s01_1704164645000_overflow1"
	normalized, err := NormalizeUsageRecordInput(input, fixedClock("2026-01-02T03:04:05.000Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Persist(context.Background(), normalized); err != nil {
		t.Fatalf("溢出落盘失败: %v", err)
	}
	instanceDirectory := filepath.Join(directory, "jobs")
	entries, err := os.ReadDir(instanceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("溢出落盘不得残留 .tmp 临时文件: %s", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			jsonFiles++
		}
	}
	if jsonFiles != 1 {
		t.Fatalf("溢出文件数 = %d, want 1", jsonFiles)
	}
	var paths []string
	entries, _ = os.ReadDir(instanceDirectory)
	for _, entry := range entries {
		paths = append(paths, filepath.Join(instanceDirectory, entry.Name()))
	}
	content, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var replay UsageRecordInput
	if err := json.Unmarshal(content, &replay); err != nil {
		t.Fatalf("溢出文件必须可 json.Unmarshal（drain 解析语义）: %v", err)
	}
	if replay.ID == "" || replay.CreatedAt == "" {
		t.Fatalf("溢出文件必须携带稳定 id/createdAt: %+v", replay)
	}
	if _, err := NormalizeUsageRecordInput(replay, nil, nil); err != nil {
		t.Fatalf("溢出文件必须通过 drain 侧归一化校验: %v", err)
	}
}

// TestWriterOverflowPersistsToSpoolInsteadOfDropping 锁定生产修复语义：
// 队列满时记录溢出落盘而不是丢弃，计数不进 droppedOverflow/droppedDispatch。
func TestWriterOverflowPersistsToSpoolInsteadOfDropping(t *testing.T) {
	store := &mockStore{}
	spool := NewFileOverflowSpool(t.TempDir())
	config := Config{
		QueueMaxItems: 1, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer := NewWriter(config, store, fixedClock("2026-01-02T03:04:05.000Z"), WithOverflowSpool(spool))
	// 不 Start：无 flush 循环，队列保持满。
	if err := writer.Enqueue(context.Background(), gatewayInput("first")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), gatewayInput("second")); err != nil {
		t.Fatal(err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedOverflowCount != 0 || runtime.DroppedDispatchCount != 0 || runtime.DroppedOversizeCount != 0 {
		t.Fatalf("溢出记录必须落盘而不是丢弃: %+v", runtime)
	}
	if runtime.QueueLength != 1 {
		t.Fatalf("队列长度 = %d, want 1（首条入队）", runtime.QueueLength)
	}
	entries, err := os.ReadDir(filepath.Join(spool.Directory, "jobs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("溢出文件必须存在: %v %v", err, entries)
	}
}

// TestWriterOverflowSpoolFailureCountsDispatchDrop 锁定 2c 行为侧：溢出落
// 盘失败时记录按 dispatch 失败计数（recordUsageRecordDispatchFailure 镜像），
// 不得重复计入 droppedOverflowCount。
func TestWriterOverflowSpoolFailureCountsDispatchDrop(t *testing.T) {
	store := &mockStore{}
	// Directory 指向一个已存在的文件 → MkdirAll 必然失败 → Persist 报错。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := NewFileOverflowSpool(blocker)
	logger := &captureLogger{}
	config := Config{
		QueueMaxItems: 1, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer := NewWriter(config, store, fixedClock("2026-01-02T03:04:05.000Z"),
		WithOverflowSpool(spool), WithLogger(logger))
	if err := writer.Enqueue(context.Background(), gatewayInput("first")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), gatewayInput("second")); err != nil {
		t.Fatal(err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedDispatchCount != 1 {
		t.Fatalf("溢出落盘失败必须计入 droppedDispatchCount: %+v", runtime)
	}
	if runtime.DroppedOverflowCount != 0 {
		t.Fatalf("溢出落盘失败不得重复计入 droppedOverflowCount: %+v", runtime)
	}
	found := false
	for _, warn := range logger.warns {
		// captureLogger 只保留 msg 文案；event 字段在 fields map 中。
		if strings.Contains(warn, "使用记录投递后台 worker 失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("溢出落盘失败必须产出 dispatch 失败日志: %v", logger.warns)
	}
}

// TestWriterPostgresPlanFastFail 锁定 B③：PG 模式缺 systemAccountId 的记录
// 在写计划期快速失败（BuildWritePlan 报错），store 不得被调用，批次按重试
// 上限转死信终态而不是到 INSERT 才撞 NOT NULL 无限重试。
func TestWriterPostgresPlanFastFail(t *testing.T) {
	store := &mockStore{}
	logger := &captureLogger{}
	config := Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 1,
		ShardRoot: t.TempDir(), Postgres: true,
	}
	writer, _ := newTestWriter(t, config, store, WithLogger(logger))
	input := gatewayInput("pg-missing-account")
	input.SystemAccountID = ""
	if err := writer.Enqueue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount == 1 })
	if store.batchCount() != 0 {
		t.Fatalf("PG plan 快速失败时 store 不得被调用: %d", store.batchCount())
	}
	dead := writer.DeadLetters()
	if len(dead) != 1 || dead[0].TraceID != "pg-missing-account" {
		t.Fatalf("死信必须保留原记录: %+v", dead)
	}
}

// TestWriterSQLiteDefaultAcceptsEmptySystemAccountID 锁定默认（SQLite）分支
// 行为不变：Postgres=false 时缺 systemAccountId 的记录照常进 store。
func TestWriterSQLiteDefaultAcceptsEmptySystemAccountID(t *testing.T) {
	store := &mockStore{}
	config := Config{
		BatchSize: 1, FlushIntervalMs: 5_000, ShardRoot: t.TempDir(),
	}
	writer, _ := newTestWriter(t, config, store)
	input := gatewayInput("sqlite-missing-account")
	input.SystemAccountID = ""
	if err := writer.Enqueue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return store.batchCount() == 1 })
	if writer.Runtime().DeadLetterCount != 0 {
		t.Fatalf("SQLite 分支不得死信: %+v", writer.Runtime())
	}
}
