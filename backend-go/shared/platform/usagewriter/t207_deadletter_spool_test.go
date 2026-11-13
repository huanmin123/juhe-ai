package usagewriter

// T207-C2：usage 记录死信与溢出落盘失败不再永久丢失。死信终态改为「先持久
// 化到溢出 spool 再出队」（任一落盘失败则整批保留队列重试）；队列满溢出落
// 盘失败改为强制回队补偿暂存。转存记录由既有 usagespooldrain 回放链路按稳
// 定 id 幂等重投（ON CONFLICT DO NOTHING）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestT207DeadLetterPersistsToSpoolBeforeDequeuing：写库重试耗尽后整批先落
// spool 成功才出队——spool 收到全部记录、pending 清空、deadLetter 计数与
// 诊断样本照旧。
func TestT207DeadLetterPersistsToSpoolBeforeDequeuing(t *testing.T) {
	store := &mockStore{failures: 100, overflowErr: errors.New("db busy")}
	spool := &mockSpool{}
	config := Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 2,
	}
	writer, _ := newTestWriter(t, config, store, WithOverflowSpool(spool))
	if err := writer.Enqueue(context.Background(), gatewayInput("t207-a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), gatewayInput("t207-b")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount == 2 })
	if writer.PendingCount() != 0 {
		t.Fatalf("dead-letter batch not dequeued after persist: %d", writer.PendingCount())
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 2 {
		t.Fatalf("spool items = %d, want 2", spooled)
	}
	dead := writer.DeadLetters()
	if len(dead) != 2 || dead[0].TraceID != "t207-a" || dead[1].TraceID != "t207-b" {
		t.Fatalf("dead letters = %+v", dead)
	}
	if runtime := writer.Runtime(); runtime.DroppedCount != 2 {
		t.Fatalf("dropped count should include dead letter: %+v", runtime)
	}
}

// t207HasWarnContaining 报告 captureLogger 是否记录过包含片段的 Warn 文案。
func t207HasWarnContaining(logger *captureLogger, fragment string) bool {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	for _, warn := range logger.warns {
		if strings.Contains(warn, fragment) {
			return true
		}
	}
	return false
}

// TestT207DeadLetterSpoolFailureKeepsBatchQueued：死信落盘失败整批保留队列
// （不计死信终态、不出队），spool 恢复后重试落盘成功、记录经 spool 转存。
// 手动装配（RetryWait 留空走默认固定退避）：死信落盘失败的重试节奏不得退
// 化为忙循环。
func TestT207DeadLetterSpoolFailureKeepsBatchQueued(t *testing.T) {
	store := &mockStore{failures: 1_000_000, overflowErr: errors.New("db busy")}
	spool := &mockSpool{failed: true}
	logger := &captureLogger{}
	writer := NewWriter(Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 2, RetryDelayMs: 5,
	}, store, fixedClock("2026-01-02T03:04:05.000Z"), WithOverflowSpool(spool), WithLogger(logger))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		writer.Close(ctx)
	})
	writer.Start()
	if err := writer.Enqueue(context.Background(), gatewayInput("t207-keep")); err != nil {
		t.Fatal(err)
	}
	// 写库重试耗尽后死信落盘失败：批次保留队列等待重试。
	eventually(2*time.Second, func() bool {
		return t207HasWarnContaining(logger, "使用记录死信落盘失败，已保留队列等待重试")
	})
	runtime := writer.Runtime()
	if runtime.DeadLetterCount != 0 {
		t.Fatalf("dead letter counted before persist: %+v", runtime)
	}
	if runtime.QueueLength != 1 {
		t.Fatalf("batch not retained on spool failure: %+v", runtime)
	}
	if runtime.DroppedDispatchCount < 1 {
		t.Fatalf("deferred counter not booked: %+v", runtime)
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 0 {
		t.Fatalf("spool items = %d, want 0", spooled)
	}

	// spool 恢复：失败预算未重置，下一轮写库失败后立即重试落盘并出队。
	spool.mu.Lock()
	spool.failed = false
	spool.mu.Unlock()
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount == 1 })
	if writer.PendingCount() != 0 {
		t.Fatalf("dead-letter batch not dequeued after retry: %d", writer.PendingCount())
	}
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if len(spool.items) != 1 || spool.items[0].TraceID != "t207-keep" {
		t.Fatalf("spool items = %+v", spool.items)
	}
}

// TestT207DeadLetterSpoolFailureThenDBRecovers：落盘失败保留的批次在写库恢
// 复后直接写库成功——记录不因 spool 短暂故障丢失。
func TestT207DeadLetterSpoolFailureThenDBRecovers(t *testing.T) {
	store := &mockStore{failures: 2, overflowErr: errors.New("db busy")}
	spool := &mockSpool{failed: true}
	logger := &captureLogger{}
	config := Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 2,
	}
	writer, _ := newTestWriter(t, config, store, WithOverflowSpool(spool), WithLogger(logger))
	if err := writer.Enqueue(context.Background(), gatewayInput("t207-back")); err != nil {
		t.Fatal(err)
	}
	// 两次写库失败耗尽重试预算，死信落盘失败保留批次；第三轮写库恢复成功。
	eventually(2*time.Second, func() bool { return store.totalRows() == 1 })
	runtime := writer.Runtime()
	if runtime.DeadLetterCount != 0 {
		t.Fatalf("retained batch must not count as dead letter: %+v", runtime)
	}
	if runtime.QueueLength != 0 {
		t.Fatalf("batch not written after recovery: %+v", runtime)
	}
	if runtime.DroppedDispatchCount < 1 {
		t.Fatalf("deferred counter not booked: %+v", runtime)
	}
	if !t207HasWarnContaining(logger, "使用记录死信落盘失败，已保留队列等待重试") {
		t.Fatal("dead-letter persist failure was not logged")
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 0 {
		t.Fatalf("spool items = %d, want 0", spooled)
	}
}

// TestT207OverflowPersistFailureRetainsRecord：队列满且溢出落盘失败时记录
// 强制回队补偿暂存（不再终态丢弃），随冲刷全部写库成功。
func TestT207OverflowPersistFailureRetainsRecord(t *testing.T) {
	store := &mockStore{}
	spool := &mockSpool{failed: true}
	logger := &captureLogger{}
	config := Config{
		QueueMaxItems: 2, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000,
	}
	writer, _ := newTestWriter(t, config, store, WithOverflowSpool(spool), WithLogger(logger))
	base := gatewayInput("t207-of")
	if err := writer.Enqueue(context.Background(), withTrace(base, "a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), withTrace(base, "b")); err != nil {
		t.Fatal(err)
	}
	// 队列已满；c 溢出且落盘失败 → 强制回队（容量瞬时超限），不报错不丢弃。
	if err := writer.Enqueue(context.Background(), withTrace(base, "c")); err != nil {
		t.Fatal(err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedDispatchCount != 1 {
		t.Fatalf("deferred dispatch counter = %+v", runtime)
	}
	if runtime.QueueLength != 3 {
		t.Fatalf("failed-spill record not retained: %+v", runtime)
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 0 {
		t.Fatalf("spool items = %d, want 0", spooled)
	}
	if got := logger.lastWarn(); !strings.Contains(got, "使用记录溢出落盘失败，已保留队列等待重试") {
		t.Fatalf("dispatch failure log = %q", got)
	}
	// 手动排空：三条记录（含强制回队的 c）全部写库成功。
	writer.Drain()
	seen := map[string]bool{}
	for _, id := range store.recordedTraceIDs() {
		seen[id] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Fatalf("record %q missing after drain: %v", want, seen)
		}
	}
}
