package publicapilogs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingLogger captures warnings for the message/cadence assertions.
type recordingLogger struct {
	mu     sync.Mutex
	warns  []string
	fields []string
}

func (l *recordingLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
	rendered := fmt.Sprint(args...)
	l.fields = append(l.fields, rendered)
}

func (l *recordingLogger) Error(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, "ERROR:"+msg)
}

func (l *recordingLogger) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, entry := range l.warns {
		if entry == msg {
			count++
		}
	}
	return count
}

// gatedWriter blocks InsertBatch until the gate is released; records every
// batch and can fail a number of leading calls.
type gatedWriter struct {
	mu      sync.Mutex
	gate    chan struct{}
	batches [][]Input
	fail    int
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{gate: make(chan struct{})}
}

func (w *gatedWriter) release() { close(w.gate) }

func (w *gatedWriter) InsertBatch(ctx context.Context, inputs []Input) error {
	select {
	case <-w.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail > 0 {
		w.fail--
		return errors.New("模拟公开接口日志批量写入失败")
	}
	w.batches = append(w.batches, append([]Input(nil), inputs...))
	return nil
}

func (w *gatedWriter) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, batch := range w.batches {
		total += len(batch)
	}
	return total
}

func (w *gatedWriter) batchSizes() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	sizes := make([]int, 0, len(w.batches))
	for _, batch := range w.batches {
		sizes = append(sizes, len(batch))
	}
	return sizes
}

func newTestPipeline(t *testing.T, writer BatchWriter, mutate func(*Config)) (*Pipeline, *manualClock) {
	t.Helper()
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	cfg := Config{RetryDelay: 5 * time.Millisecond, Now: clock.Now}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewPipeline(writer, cfg), clock
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", message)
}

// TestPipelineBatchingVerifiesBatchSize pins the batch contract: every batch
// is at most 50 records (the Node flushBatchSize) and the whole queue drains.
// The exact partition is timing dependent in Node too (the flusher runs as
// records arrive), so it is not asserted.
func TestPipelineBatchingVerifiesBatchSize(t *testing.T) {
	writer := newGatedWriter()
	pipeline, _ := newTestPipeline(t, writer, nil)
	const total = 120
	for i := 0; i < total; i++ {
		if !pipeline.Enqueue(Input{ID: fmt.Sprintf("publog_b%d", i), Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}) {
			t.Fatalf("enqueue %d rejected", i)
		}
	}
	writer.release()
	pipeline.Close(context.Background())
	if got := writer.total(); got != total {
		t.Fatalf("written %d, want %d", got, total)
	}
	sizes := writer.batchSizes()
	if len(sizes) < 3 {
		t.Fatalf("expected at least 3 batches for %d records, got %v", total, sizes)
	}
	for _, size := range sizes {
		if size > DefaultFlushBatchSize {
			t.Fatalf("batch size %d exceeds the limit of %d: %v", size, DefaultFlushBatchSize, sizes)
		}
	}
}

// TestPipelineOverflowDrop mirrors the Node queue-full semantics: the newest
// record is dropped and counted, warnings fire on the first drop and every
// 100th.
func TestPipelineOverflowDrop(t *testing.T) {
	writer := newGatedWriter()
	logger := &recordingLogger{}
	pipeline, _ := newTestPipeline(t, writer, func(cfg *Config) {
		cfg.QueueMaxItems = 2
		cfg.Logger = logger
	})

	if !pipeline.Enqueue(Input{ID: "in-1", Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}) {
		t.Fatal("first enqueue must succeed")
	}
	dropped := 0
	for i := 0; i < 201; i++ {
		if pipeline.Enqueue(Input{ID: fmt.Sprintf("drop-%d", i), Method: "GET", Path: "/overflow", TraceID: "overflow", StartedAt: "s", EndedAt: "e"}) {
			dropped++
		}
	}
	runtime := pipeline.Runtime()
	if runtime.QueueLength != 2 {
		t.Fatalf("queue length: %d", runtime.QueueLength)
	}
	if runtime.DroppedCount != 200 {
		t.Fatalf("dropped: %d", runtime.DroppedCount)
	}
	// Warning cadence: drop 1, 100, 200.
	if got := logger.count("公开接口日志队列已满，丢弃日志记录"); got != 3 {
		t.Fatalf("overflow warnings: %d", got)
	}
	writer.release()
	pipeline.Close(context.Background())
	if got := writer.total(); got != 2 {
		t.Fatalf("written after release: %d", got)
	}
	if pipeline.Runtime().DroppedCount != 200 {
		t.Fatalf("dropped counter after drain: %d", pipeline.Runtime().DroppedCount)
	}
}

// TestPipelineByteBudgetDrop mirrors the oversized-record drop.
func TestPipelineByteBudgetDrop(t *testing.T) {
	writer := newGatedWriter()
	pipeline, _ := newTestPipeline(t, writer, func(cfg *Config) {
		cfg.QueueMaxBytes = 200
	})
	big := Input{ID: "big", Method: "POST", Path: "/x", StartedAt: "s", EndedAt: "e",
		RequestData: map[string]any{"blob": strings.Repeat("x", 500)}}
	if pipeline.Enqueue(big) {
		t.Fatal("oversized record must be dropped")
	}
	if pipeline.Runtime().DroppedCount != 1 || pipeline.Runtime().QueueLength != 0 {
		t.Fatalf("runtime: %+v", pipeline.Runtime())
	}
	small := Input{ID: "small", Method: "POST", Path: "/x", StartedAt: "s", EndedAt: "e"}
	if !pipeline.Enqueue(small) {
		t.Fatal("small record must fit")
	}
	writer.release()
	pipeline.Close(context.Background())
	if got := writer.total(); got != 1 {
		t.Fatalf("written: %d", got)
	}
}

// flakyStore fails the leading InsertBatch calls, then delegates to the real
// store: the retry must land the retained batch without partial writes.
type flakyStore struct {
	store          BatchWriter
	mu             sync.Mutex
	remainingFails int
}

func (f *flakyStore) InsertBatch(ctx context.Context, inputs []Input) error {
	f.mu.Lock()
	fail := f.remainingFails > 0
	if fail {
		f.remainingFails--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("模拟公开接口日志批量写入失败")
	}
	return f.store.InsertBatch(ctx, inputs)
}

// TestPipelineWriteFailureRetainsAndRetries mirrors the failed-batch retry:
// nothing partial is written, the batch is retained and the retry succeeds.
func TestPipelineWriteFailureRetainsAndRetries(t *testing.T) {
	store, db, _ := newTestStore(t)
	logger := &recordingLogger{}
	writer := &flakyStore{store: store, remainingFails: 2}
	pipeline, _ := newTestPipeline(t, writer, func(cfg *Config) {
		cfg.Logger = logger
	})

	if !pipeline.Enqueue(Input{ID: "retry-1", Method: "GET", Path: "/x", TraceID: "trace-retry", StartedAt: "s", EndedAt: "e"}) {
		t.Fatal("enqueue")
	}
	waitFor(t, 2*time.Second, func() bool {
		return pipeline.Runtime().QueueLength == 0
	}, "retry to drain the queue")

	if count := countRows(t, db, "trace-retry"); count != 1 {
		t.Fatalf("written rows after retry: %d", count)
	}
	if pipeline.Runtime().FlushFailureCount == 0 {
		t.Fatal("flush failure counter must be bumped")
	}
	if got := logger.count("公开接口日志批量写入失败，已保留批次等待重试"); got == 0 {
		t.Fatal("write-failure warning missing")
	}
	pipeline.Close(context.Background())
}

// TestPipelineCloseDrainsEverything is the graceful-shutdown gate: records
// already in the queue must survive Close.
func TestPipelineCloseDrainsEverything(t *testing.T) {
	writer := newGatedWriter()
	pipeline, _ := newTestPipeline(t, writer, nil)
	const total = 137
	for i := 0; i < total; i++ {
		if !pipeline.Enqueue(Input{ID: fmt.Sprintf("publog_d%d", i), Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}) {
			t.Fatalf("enqueue %d rejected", i)
		}
	}
	writer.release()
	pipeline.Close(context.Background())
	if got := writer.total(); got != total {
		t.Fatalf("drain lost records: %d/%d", got, total)
	}
	ids := map[string]bool{}
	writer.mu.Lock()
	for _, batch := range writer.batches {
		for _, input := range batch {
			ids[input.ID] = true
		}
	}
	writer.mu.Unlock()
	if len(ids) != total {
		t.Fatalf("unique ids: %d", len(ids))
	}
}

// TestPipelineCloseWithFailingWriterBoundsShutdown proves shutdown can never
// hang on a broken store: the drain attempts batches, stops on failure and
// Close returns promptly.
func TestPipelineCloseWithFailingWriterBoundsShutdown(t *testing.T) {
	writer := newGatedWriter()
	pipeline, _ := newTestPipeline(t, writer, func(cfg *Config) {
		cfg.RetryDelay = 50 * time.Millisecond
	})
	for i := 0; i < 5; i++ {
		if !pipeline.Enqueue(Input{ID: fmt.Sprintf("fail-%d", i), Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}) {
			t.Fatal("enqueue")
		}
	}
	// The writer never releases its gate: every InsertBatch blocks forever, so
	// Close's context bounds the wait instead. Release on cleanup so the
	// worker goroutine can finish.
	t.Cleanup(writer.release)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	pipeline.Close(ctx)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close hung for %v", elapsed)
	}
	if pipeline.Runtime().QueueLength == 0 {
		t.Fatal("blocked writer must leave records unwritten")
	}
}

// TestPipelineEnqueueAfterCloseIsRejected pins post-shutdown behavior.
func TestPipelineEnqueueAfterCloseIsRejected(t *testing.T) {
	writer := newGatedWriter()
	logger := &recordingLogger{}
	pipeline, _ := newTestPipeline(t, writer, func(cfg *Config) { cfg.Logger = logger })
	writer.release()
	pipeline.Close(context.Background())
	if pipeline.Enqueue(Input{Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}) {
		t.Fatal("enqueue after close must be rejected")
	}
	if pipeline.Runtime().DroppedCount != 1 {
		t.Fatalf("runtime: %+v", pipeline.Runtime())
	}
}
