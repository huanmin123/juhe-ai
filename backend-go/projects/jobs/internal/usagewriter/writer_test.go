package usagewriter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockStore records every WriteBatch plan and can fail the first N calls.
type mockStore struct {
	mu          sync.Mutex
	batches     []WritePlan
	inserted    []int
	failures    int
	callCount   int
	overflowErr error
}

func (m *mockStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.failures > 0 {
		m.failures--
		return 0, m.overflowErr
	}
	m.batches = append(m.batches, plan)
	m.inserted = append(m.inserted, len(plan.ShardEntries))
	return len(plan.ShardEntries), nil
}

func (m *mockStore) batchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.batches)
}

func (m *mockStore) totalRows() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, batch := range m.batches {
		total += len(batch.ShardEntries)
	}
	return total
}

func (m *mockStore) recordedShardKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for _, batch := range m.batches {
		for _, shard := range batch.RowsByShard {
			keys = append(keys, shard.Location.ShardKey)
		}
	}
	return keys
}

func (m *mockStore) recordedTraceIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, batch := range m.batches {
		for _, entry := range batch.ShardEntries {
			ids = append(ids, entry.TraceID)
		}
	}
	return ids
}

// immediateRetry skips the fixed backoff and counts the waits.
type immediateRetry struct {
	mu    sync.Mutex
	calls int
}

func (r *immediateRetry) wait(ctx Ctx, delay time.Duration) bool {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return true
}

func (r *immediateRetry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// mockSpool records the overflow compensation persists.
type mockSpool struct {
	mu     sync.Mutex
	items  []UsageRecordInput
	failed bool
}

func (s *mockSpool) Persist(ctx Ctx, input UsageRecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("spool disk full")
	}
	s.items = append(s.items, input)
	return nil
}

func gatewayInput(traceID string) UsageRecordInput {
	return UsageRecordInput{
		SystemAccountID: "sys1",
		TraceID:         traceID,
		TrafficSource:   TrafficSourceGateway,
		Success:         true,
		Model:           "gpt-5",
	}
}

func eventually(timeout time.Duration, condition func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestWriter(t *testing.T, config Config, store ShardStore, options ...Option) (*Writer, *immediateRetry) {
	t.Helper()
	writer := NewWriter(config, store, fixedClock("2026-01-02T03:04:05.000Z"), options...)
	retry := &immediateRetry{}
	writer.RetryWait = retry.wait
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		writer.Close(ctx)
	})
	writer.Start()
	return writer, retry
}

func TestWriterNormalBatchWrite(t *testing.T) {
	store := &mockStore{}
	config := Config{BatchSize: 4, FlushIntervalMs: 5_000, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	for i := 0; i < 4; i++ {
		if err := writer.Enqueue(context.Background(), gatewayInput(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	eventually(2*time.Second, func() bool { return store.totalRows() == 4 })
	if writer.PendingCount() != 0 {
		t.Fatalf("queue not drained: %d", writer.PendingCount())
	}
	if store.batchCount() != 1 {
		t.Fatalf("batch count = %d, want 1", store.batchCount())
	}
	keys := store.recordedShardKeys()
	if len(keys) == 0 || !strings.HasPrefix(keys[0], "20260102:s") {
		t.Fatalf("shard keys = %v", keys)
	}
	runtime := writer.Runtime()
	if runtime.WrittenRecords != 4 || runtime.HandledRecords != 4 {
		t.Fatalf("runtime counters = %+v", runtime)
	}
	if runtime.DroppedCount != 0 || runtime.FlushFailureCount != 0 {
		t.Fatalf("unexpected drops/failures: %+v", runtime)
	}
	// Batch rows carry the shard-routed plan with the normalized record.
	batch := store.batches[0]
	if len(batch.RowsByShard[0].Rows) != 4 {
		t.Fatalf("batch rows = %d", len(batch.RowsByShard[0].Rows))
	}
	if batch.ShardEntries[0].CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("entry createdAt = %q", batch.ShardEntries[0].CreatedAt)
	}
}

func TestWriterShardDistribution(t *testing.T) {
	store := &mockStore{}
	config := Config{BatchSize: 100, FlushIntervalMs: 5_000, ShardCount: 8, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	// Explicit pre-generated ids spread over shard buckets deterministically.
	inputs := []UsageRecordInput{}
	for shard := 0; shard < 8; shard++ {
		inputs = append(inputs, UsageRecordInput{
			SystemAccountID: "sys1", TraceID: string(rune('a' + shard)), TrafficSource: TrafficSourceGateway, Success: true,
			ID:        "usage_20260102_s" + FormatShardID(shard) + "_1767225600000_x" + FormatShardID(shard),
			CreatedAt: "2026-01-02T03:04:05.000Z",
		})
	}
	for _, input := range inputs {
		if err := writer.Enqueue(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	// Below the batch size the writer waits for the interval; Close drives
	// the shutdown drain deterministically.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	writer.Close(ctx)
	if got := store.totalRows(); got != 8 {
		t.Fatalf("rows = %d, want 8", got)
	}
	keys := store.recordedShardKeys()
	if len(keys) != 8 {
		t.Fatalf("shard group count = %d (%v)", len(keys), keys)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("shard key repeated across group batches: %v", keys)
		}
		seen[key] = true
		if !strings.HasPrefix(key, "20260102:s") {
			t.Fatalf("key format = %q", key)
		}
	}
}

func TestWriterRetryThenTerminalDeadLetter(t *testing.T) {
	store := &mockStore{failures: 2, overflowErr: errors.New("db busy")}
	config := Config{
		BatchSize: 1, FlushIntervalMs: 5_000, MaxWriteAttempts: 4, ShardRoot: t.TempDir(),
	}
	writer, retry := newTestWriter(t, config, store)
	// Batch 1 (records a,b) survives two failures then succeeds.
	if err := writer.Enqueue(context.Background(), gatewayInput("a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), gatewayInput("b")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return store.totalRows() == 2 })
	ids := store.recordedTraceIDs()
	if len(ids) < 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("order not preserved: %v", ids)
	}
	if retry.count() != 2 {
		t.Fatalf("retry waits = %d, want 2", retry.count())
	}
	runtime := writer.Runtime()
	if runtime.FlushFailureCount != 0 {
		t.Fatalf("flush failure counter not reset: %+v", runtime)
	}

	// Batch 2 exhausts the attempt cap: dead-letter terminal state.
	store.mu.Lock()
	store.failures = 100
	store.mu.Unlock()
	if err := writer.Enqueue(context.Background(), gatewayInput("dead")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount == 1 })
	if writer.PendingCount() != 0 {
		t.Fatalf("dead-letter batch still queued: %d", writer.PendingCount())
	}
	dead := writer.DeadLetters()
	if len(dead) != 1 || dead[0].TraceID != "dead" {
		t.Fatalf("dead letters = %+v", dead)
	}
	if writer.Runtime().DroppedCount != 1 {
		t.Fatalf("dropped count should include dead letter: %+v", writer.Runtime())
	}
}

func TestWriterZeroAttemptsRetriesForeverUntilStop(t *testing.T) {
	// MaxWriteAttempts=0 (Node default): a failing batch is never dropped;
	// Close() drains what it can and the failed batch stays terminal-queued.
	store := &mockStore{failures: 1 << 20, overflowErr: errors.New("db down")}
	config := Config{BatchSize: 1, FlushIntervalMs: 5_000, ShardRoot: t.TempDir()}
	writer, retry := newTestWriter(t, config, store)
	if err := writer.Enqueue(context.Background(), gatewayInput("stuck")); err != nil {
		t.Fatal(err)
	}
	eventually(2*time.Second, func() bool { return retry.count() > 3 })
	if writer.Runtime().DeadLetterCount != 0 {
		t.Fatal("records must never dead-letter with unlimited retries")
	}
	if writer.PendingCount() != 1 {
		t.Fatalf("failed record must stay queued: %d", writer.PendingCount())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writer.Close(ctx)
	if writer.Runtime().DeadLetterCount != 0 {
		t.Fatal("close must not dead-letter")
	}
}

func TestWriterOverflowDropAndSpool(t *testing.T) {
	store := &mockStore{}
	spool := &mockSpool{}
	logger := &captureLogger{}
	config := Config{
		QueueMaxItems: 2, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer, _ := newTestWriter(t, config, store, WithOverflowSpool(spool), WithLogger(logger))
	base := gatewayInput("x")
	// Two records fit; the third overflows the item cap and is spooled
	// instead of counted (spool succeeds).
	if err := writer.Enqueue(context.Background(), withTrace(base, "a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), withTrace(base, "b")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Enqueue(context.Background(), withTrace(base, "c")); err != nil {
		t.Fatal(err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedOverflowCount != 0 {
		t.Fatalf("spooled record counted as dropped: %+v", runtime)
	}
	spool.mu.Lock()
	spooled := len(spool.items)
	spool.mu.Unlock()
	if spooled != 1 {
		t.Fatalf("spool items = %d", spooled)
	}

	// A spool failure drops the record with the dispatch counter and the
	// Node log copy.
	spool.mu.Lock()
	spool.failed = true
	spool.mu.Unlock()
	if err := writer.Enqueue(context.Background(), withTrace(base, "d")); err != nil {
		t.Fatal(err)
	}
	runtime = writer.Runtime()
	if runtime.DroppedDispatchCount != 1 {
		t.Fatalf("dispatch drop counter = %+v", runtime)
	}
	if got := logger.lastWarn(); !strings.Contains(got, "使用记录投递后台 worker 失败，已跳过投递") {
		t.Fatalf("dispatch failure log = %q", got)
	}

	// An oversize record (bytes above the whole queue budget) drops as
	// oversize without consulting the spool.
	huge := withTrace(base, "huge")
	huge.ErrorMessage = strings.Repeat("x", config.QueueMaxBytes+10)
	if err := writer.Enqueue(context.Background(), huge); err != nil {
		t.Fatal(err)
	}
	runtime = writer.Runtime()
	if runtime.DroppedOversizeCount != 1 {
		t.Fatalf("oversize counter = %+v", runtime)
	}
	spool.mu.Lock()
	spooled = len(spool.items)
	spool.mu.Unlock()
	if spooled != 1 {
		t.Fatalf("oversize record must bypass the spool: %d", spooled)
	}
}

func withTrace(base UsageRecordInput, traceID string) UsageRecordInput {
	base.TraceID = traceID
	return base
}

type captureLogger struct {
	mu     sync.Mutex
	warns  []string
	errors []string
}

func (l *captureLogger) Warn(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *captureLogger) Error(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}

func (l *captureLogger) lastWarn() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.warns) == 0 {
		return ""
	}
	return l.warns[len(l.warns)-1]
}

func (l *captureLogger) lastError() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.errors) == 0 {
		return ""
	}
	return l.errors[len(l.errors)-1]
}

func (l *captureLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

func (l *captureLogger) warnCountContaining(needle string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, warn := range l.warns {
		if strings.Contains(warn, needle) {
			count++
		}
	}
	return count
}

func TestWriterDropLogSampling(t *testing.T) {
	store := &mockStore{}
	logger := &captureLogger{}
	config := Config{
		QueueMaxItems: 1, QueueMaxBytes: 64 * 1024, BatchSize: 10,
		FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
	}
	writer, _ := newTestWriter(t, config, store, WithLogger(logger))
	// Fill the queue; every further record overflows. The sampled log only
	// fires at counts 1..10 and multiples of 100.
	if err := writer.Enqueue(context.Background(), withTrace(gatewayInput("first"), "first")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 112; i++ {
		if err := writer.Enqueue(context.Background(), withTrace(gatewayInput("x"), "x"+string(rune('a'+i%26)))); err != nil {
			t.Fatal(err)
		}
	}
	runtime := writer.Runtime()
	if runtime.DroppedOverflowCount != 112 {
		t.Fatalf("overflow count = %d", runtime.DroppedOverflowCount)
	}
	// Drops 1-10 log, 11-111 skip, 112 skipped (not a multiple of 100).
	// Only the drop copy counts; saturation warnings use their own copy.
	// Node sampling: counts 1..10 log, 11..99 skip, 100 logs again
	// (`droppedCount > 10 && % 100 !== 0` returns early).
	if got := logger.warnCountContaining("使用记录队列达到保护上限"); got != 11 {
		t.Fatalf("sampled drop logs = %d, want 11", got)
	}
	if got := logger.lastWarn(); !strings.Contains(got, "使用记录队列达到保护上限，已丢弃新记录") {
		t.Fatalf("drop log copy = %q", got)
	}
}

func TestWriterGracefulShutdownDrain(t *testing.T) {
	store := &mockStore{}
	config := Config{
		BatchSize: 2, FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
		RetryDelayMs: 50,
	}
	writer, _ := newTestWriter(t, config, store)
	for i := 0; i < 7; i++ {
		if err := writer.Enqueue(context.Background(), gatewayInput(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	writer.Close(ctx)
	if got := store.totalRows(); got != 7 {
		t.Fatalf("shutdown drain wrote %d/7 rows", got)
	}
	if writer.PendingCount() != 0 {
		t.Fatalf("pending after shutdown: %d", writer.PendingCount())
	}
	// Enqueue after close is rejected.
	if err := writer.Enqueue(context.Background(), gatewayInput("late")); err == nil {
		t.Fatal("enqueue after close must fail")
	}
}

func TestWriterShutdownDrainStopsOnFailure(t *testing.T) {
	store := &mockStore{failures: 1 << 20, overflowErr: errors.New("db down")}
	config := Config{BatchSize: 1, FlushIntervalMs: 60_000, ShutdownFlushMaxBatches: 3, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	for i := 0; i < 3; i++ {
		if err := writer.Enqueue(context.Background(), gatewayInput(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writer.Close(ctx)
	// The failing batch blocks the drain (retryOnFailure=false) and stays
	// queued for diagnostics; no data silently vanishes.
	if store.totalRows() != 0 {
		t.Fatalf("failing store wrote rows: %d", store.totalRows())
	}
	if writer.PendingCount() != 3 {
		t.Fatalf("pending after failed shutdown = %d", writer.PendingCount())
	}
}

func TestWriterFreezesPricingAtEnqueue(t *testing.T) {
	store := &mockStore{}
	catalog := &mockCatalog{
		resolveModel: "gpt-5",
		breakdown: &CostBreakdown{
			AccountChargeUsd:         floatPtr(0.42),
			Multiplier:               1,
			ServiceTierPricingSource: TierSourceDefault,
		},
	}
	config := Config{
		BatchSize: 10, FlushIntervalMs: 60_000, ShardRoot: t.TempDir(),
		FreezePricing: true, CatalogSnapshot: true,
	}
	writer, _ := newTestWriter(t, config, store, WithCatalog(catalog))
	input := gatewayInput("frozen")
	input.ProviderCode = "gpt"
	input.InputTokens = intPtr(9)
	if err := writer.Enqueue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	// 冻结发生在入队时点：enqueue 返回时目录已被调用一次。
	if catalog.breakdownCalls != 1 {
		t.Fatalf("freeze-time breakdown calls = %d", catalog.breakdownCalls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	writer.Close(ctx)
	if got := store.totalRows(); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	entry := store.batches[0].ShardEntries[0]
	if entry.CostUsd == nil || *entry.CostUsd != 0.42 {
		t.Fatalf("frozen cost not persisted: %+v", entry.CostUsd)
	}
	row := store.batches[0].RowsByShard[0].Rows[0]
	snapshot, ok := row.Params[columnIndexOf("cost_breakdown_snapshot_json")].(string)
	if !ok || !strings.Contains(snapshot, `"accountChargeUsd":0.42`) {
		t.Fatalf("snapshot json = %v", snapshot)
	}
}

func TestWriterConcurrentEnqueueRace(t *testing.T) {
	store := &mockStore{}
	config := Config{BatchSize: 16, FlushIntervalMs: 5, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := writer.Enqueue(context.Background(), gatewayInput(string(rune('a'+worker))+itoa(i))); err != nil {
					t.Errorf("enqueue: %v", err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	eventually(3*time.Second, func() bool { return store.totalRows() == 200 })
	if got := writer.Runtime().WrittenRecords; got != 200 {
		t.Fatalf("written = %d, want 200", got)
	}
}

func TestWriterInvalidNormalizeInput(t *testing.T) {
	store := &mockStore{}
	config := Config{BatchSize: 4, FlushIntervalMs: 60_000, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	input := gatewayInput("bad")
	input.CreatedAt = "not-a-time"
	if err := writer.Enqueue(context.Background(), input); err == nil {
		t.Fatal("expected normalize error")
	}
	if writer.PendingCount() != 0 {
		t.Fatal("rejected input must not be queued")
	}
}

func TestWriterSlowFlushMetrics(t *testing.T) {
	// The store stub sleeps past the slow threshold to exercise the
	// slow-flush counters.
	store := &slowStore{delay: 520 * time.Millisecond}
	config := Config{BatchSize: 1, FlushIntervalMs: 5_000, ShardRoot: t.TempDir()}
	writer, _ := newTestWriter(t, config, store)
	if err := writer.Enqueue(context.Background(), gatewayInput("s")); err != nil {
		t.Fatal(err)
	}
	eventually(4*time.Second, func() bool { return writer.Runtime().SlowFlushCount > 0 })
	runtime := writer.Runtime()
	if runtime.LastFlushMs < 500 || runtime.MaxFlushMs < 500 {
		t.Fatalf("flush duration metrics = %+v", runtime)
	}
	if runtime.LastSlowFlushAt == "" {
		t.Fatal("missing slow flush timestamp")
	}
}

type slowStore struct{ delay time.Duration }

func (s *slowStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	time.Sleep(s.delay)
	return len(plan.ShardEntries), nil
}
