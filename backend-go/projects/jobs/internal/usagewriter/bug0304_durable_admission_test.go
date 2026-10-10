package usagewriter

// BUG-0304 回归测试：EnqueueDurable 的 durable 交接契约（nil ⇔ 已接受，
// 非 nil ⇔ 未接收且不终态计数）。Enqueue 的 best-effort 语义由既有
// TestWriterOverflowDropAndSpool / TestWriterOverflowSpoolFailureCountsDispatchDrop
// 锁定，本文件只覆盖新端口。
//
// 六场景覆盖归属（BUG-0304 验收）：
//  1. 正常入队            → 既有 TestWriterNormalBatchWrite + 本文件
//    TestBug0304EnqueueDurableAdmitsWhenQueueHasRoom；
//  2. 溢出持久化成功      → 既有 TestWriterOverflowPersistsToSpoolInsteadOfDropping
//    （Enqueue）+ 本文件 TestBug0304EnqueueDurableOverflowsToSpoolReturnsNil；
//  3. 溢出失败保源        → 本文件
//    TestBug0304EnqueueDurableOverflowPersistFailureReturnsError +
//    usagespooldrain/bug0304_durable_source_test.go（端到端）；
//  4. 重试恢复            → usagespooldrain/bug0304_durable_source_test.go；
//  5. 稳定 ID 幂等        → 既有 TestDrainIntoWriterEndToEnd /
//    TestFileOverflowSpoolWritesReplayableRecord（接口更名后仍绿）；
//  6. 停机排空            → 既有 TestWriterGracefulShutdownDrain /
//    TestWriterShutdownDrainStopsOnFailure + 本文件
//    TestBug0304EnqueueDurableAfterStopReturnsError。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newFullQueueWriter 构造不启动 flush 循环的 writer 并以一条记录占满队列
// （QueueMaxItems=1），返回 writer 供溢出分支断言。
func newFullQueueWriter(t *testing.T, options ...Option) *Writer {
	t.Helper()
	config := Config{
		QueueMaxItems: 1, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer := NewWriter(config, &mockStore{}, fixedClock("2026-01-02T03:04:05.000Z"), options...)
	// 不 Start：无 flush 循环，队列保持满。
	if err := writer.Enqueue(context.Background(), gatewayInput("first")); err != nil {
		t.Fatal(err)
	}
	return writer
}

// TestBug0304EnqueueDurableOverflowPersistFailureReturnsError 锁定核心修
// 复：队列满且溢出落盘失败时返回可重试错误，不递增终态丢弃计数、不写丢弃
// 日志（记录未接收，由 durable 源文件重试）。
func TestBug0304EnqueueDurableOverflowPersistFailureReturnsError(t *testing.T) {
	// Directory 指向一个已存在的普通文件 → MkdirAll 必然失败 → Persist 报错
	// （同 wb_overflow_pgfastfail_test.go 手法）。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := NewFileOverflowSpool(blocker)
	logger := &captureLogger{}
	writer := newFullQueueWriter(t, WithOverflowSpool(spool), WithLogger(logger))

	err := writer.EnqueueDurable(context.Background(), gatewayInput("second"))
	if err == nil {
		t.Fatal("队列满且溢出落盘失败时 EnqueueDurable 必须返回错误")
	}
	if !strings.Contains(err.Error(), "usage writer 溢出落盘失败，durable 源记录未接收") {
		t.Fatalf("错误必须保留可重试语义的原始原因: %v", err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedDispatchCount != 0 {
		t.Fatalf("未接收记录不得计入 droppedDispatchCount: %+v", runtime)
	}
	if runtime.DroppedOverflowCount != 0 {
		t.Fatalf("未接收记录不得计入 droppedOverflowCount: %+v", runtime)
	}
	// 占满首条的 Enqueue 会触发饱和 Warn（1/1 ≥ 80%）；EnqueueDurable 自身
	// 不得追加丢弃文案日志（dispatch 失败与队列保护上限，drain 侧已有
	// usage_record_spool_flush_failed Error）。
	if got := logger.warnCountContaining("使用记录投递后台 worker 失败"); got != 0 {
		t.Fatalf("EnqueueDurable 不得写 dispatch 失败日志: %v", logger.warns)
	}
	if got := logger.warnCountContaining("使用记录队列达到保护上限"); got != 0 {
		t.Fatalf("EnqueueDurable 不得写丢弃日志: %v", logger.warns)
	}
	if writer.PendingCount() != 1 {
		t.Fatalf("队列长度 = %d, want 1（首条仍占位）", writer.PendingCount())
	}
}

// TestBug0304EnqueueDurableOverflowsToSpoolReturnsNil 锁定队列满且溢出落
// 盘成功时返回 nil（已接受，可回放）。
func TestBug0304EnqueueDurableOverflowsToSpoolReturnsNil(t *testing.T) {
	spool := NewFileOverflowSpool(t.TempDir())
	writer := newFullQueueWriter(t, WithOverflowSpool(spool))

	if err := writer.EnqueueDurable(context.Background(), gatewayInput("second")); err != nil {
		t.Fatalf("溢出落盘成功必须返回 nil: %v", err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedDispatchCount != 0 || runtime.DroppedOverflowCount != 0 {
		t.Fatalf("溢出落盘成功不是丢弃: %+v", runtime)
	}
	entries, err := os.ReadDir(filepath.Join(spool.Directory, "jobs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("溢出文件必须存在: %v %v", err, entries)
	}
}

// TestBug0304EnqueueDurableQueueFullWithoutSpoolReturnsError 锁定队列满且
// 未配置溢出 spool 时返回可重试错误（不丢弃）。
func TestBug0304EnqueueDurableQueueFullWithoutSpoolReturnsError(t *testing.T) {
	writer := newFullQueueWriter(t) // 无 WithOverflowSpool

	err := writer.EnqueueDurable(context.Background(), gatewayInput("second"))
	if err == nil {
		t.Fatal("队列满且未配置 spool 时 EnqueueDurable 必须返回错误")
	}
	if !strings.Contains(err.Error(), "durable 交接不丢弃") {
		t.Fatalf("错误必须表达 durable 不丢弃语义: %v", err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedOverflowCount != 0 || runtime.DroppedDispatchCount != 0 {
		t.Fatalf("未接收记录不得计入丢弃计数: %+v", runtime)
	}
}

// TestBug0304EnqueueDurableAfterStopReturnsError 锁定 writer 已停时与
// Enqueue 同款错误形态。
func TestBug0304EnqueueDurableAfterStopReturnsError(t *testing.T) {
	store := &mockStore{}
	config := Config{BatchSize: 4, FlushIntervalMs: 60_000, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	writer.Close(ctx)

	err := writer.EnqueueDurable(context.Background(), gatewayInput("late"))
	if err == nil {
		t.Fatal("writer 已停时 EnqueueDurable 必须返回错误")
	}
	if err.Error() != "usage writer 已停止，拒绝写入使用记录" {
		t.Fatalf("stopped 错误文案必须与 Enqueue 同款: %q", err.Error())
	}
}

// TestBug0304EnqueueDurableOversizeTerminalDrop 锁定 oversize 分支与
// Enqueue 同款：终态丢弃计数、返回 nil、不咨询 spool。
func TestBug0304EnqueueDurableOversizeTerminalDrop(t *testing.T) {
	store := &mockStore{}
	spool := &mockSpool{}
	config := Config{
		QueueMaxItems: 10, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer, _ := newTestWriter(t, config, store, WithOverflowSpool(spool))
	huge := gatewayInput("huge")
	huge.ErrorMessage = strings.Repeat("x", config.QueueMaxBytes+10)

	if err := writer.EnqueueDurable(context.Background(), huge); err != nil {
		t.Fatalf("oversize 终态丢弃必须返回 nil: %v", err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedOversizeCount != 1 {
		t.Fatalf("oversize counter = %+v", runtime)
	}
	if runtime.DroppedDispatchCount != 0 || runtime.DroppedOverflowCount != 0 {
		t.Fatalf("oversize 不得走溢出计数: %+v", runtime)
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 0 {
		t.Fatalf("oversize 记录不得咨询 spool: %d", spooled)
	}
}

// TestBug0304EnqueueDurableAdmitsWhenQueueHasRoom 锁定队列未满时与 Enqueue
// 同款准入尾部：入队、经 flush 落库、计数正确。
func TestBug0304EnqueueDurableAdmitsWhenQueueHasRoom(t *testing.T) {
	store := &mockStore{}
	// BatchSize=1：单条即批满立即冲刷，不等 flush ticker，断言确定。
	config := Config{BatchSize: 1, FlushIntervalMs: 5_000, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)

	if err := writer.EnqueueDurable(context.Background(), gatewayInput("durable-1")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return store.totalRows() == 1 })
	runtime := writer.Runtime()
	if runtime.HandledRecords != 1 || runtime.WrittenRecords != 1 {
		t.Fatalf("runtime counters = %+v", runtime)
	}
	if runtime.DroppedCount != 0 {
		t.Fatalf("正常准入不得有丢弃: %+v", runtime)
	}
}
