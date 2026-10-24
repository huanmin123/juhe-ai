package auditlog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is a scriptable Store mock for the producer unit tests. It records
// the observed call order (renew → persist → hot search) and replays the
// failure branches the producer must degrade on.
type fakeStore struct {
	mu sync.Mutex

	renewCalls   int
	persistCalls int
	hotCalls     int
	retractCalls int

	renewOK  bool
	renewErr error
	// renewFailFirst 让前 N 次续约失败（w13g：复现首次续约超时、撤回路径
	// 续约恢复的生产形态）；默认 0 不改变既有行为。
	renewFailFirst int
	persistErr     error
	persistIgn     bool
	hotErr         error
	retractErr     error

	persisted  []AuditLogInput
	hotInputs  [][]AuditLogInput
	retractIDs []string

	signal  chan struct{}
	signalN int
}

func (f *fakeStore) counts() (renews, persists, hots int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls, f.persistCalls, f.hotCalls
}

func (f *fakeStore) persistedInputs() []AuditLogInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AuditLogInput(nil), f.persisted...)
}

func (f *fakeStore) waitForCalls(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.countCalls() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d store calls, got %d", want, f.countCalls())
}

func (f *fakeStore) countCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls + f.persistCalls + f.hotCalls + f.retractCalls
}

func (f *fakeStore) EnsureSchema(context.Context) error { return nil }

func (f *fakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, true, nil
}

func (f *fakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	f.mu.Lock()
	f.renewCalls++
	fail := f.renewCalls <= f.renewFailFirst
	f.mu.Unlock()
	if fail {
		return false, errors.New("续约 F3 PostgreSQL owner lease 失败: timeout: context deadline exceeded")
	}
	return f.renewOK, f.renewErr
}

func (f *fakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error { return nil }

func (f *fakeStore) CleanupOwnedBlobTemps(context.Context, OwnerLease, time.Time) error { return nil }

func (f *fakeStore) CleanupOrphanedBlobTemps(context.Context, OwnerLease, time.Time) error {
	return nil
}

func (f *fakeStore) Persist(_ context.Context, _ OwnerLease, input AuditLogInput) (PersistResult, error) {
	f.mu.Lock()
	f.persistCalls++
	f.persisted = append(f.persisted, input)
	f.mu.Unlock()
	if f.persistErr != nil {
		return PersistResult{}, f.persistErr
	}
	return PersistResult{Ignored: f.persistIgn}, nil
}

func (f *fakeStore) CleanupRetention(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error) {
	return RetentionResult{}, nil
}

func (f *fakeStore) AppendHotSearch(_ context.Context, _ OwnerLease, inputs []AuditLogInput) (int, error) {
	f.mu.Lock()
	f.hotCalls++
	f.hotInputs = append(f.hotInputs, append([]AuditLogInput(nil), inputs...))
	f.mu.Unlock()
	if f.hotErr != nil {
		return 0, f.hotErr
	}
	return len(inputs), nil
}

func (f *fakeStore) RetractInProgress(_ context.Context, _ OwnerLease, auditLogID string) (bool, error) {
	f.mu.Lock()
	f.retractCalls++
	f.retractIDs = append(f.retractIDs, auditLogID)
	err := f.retractErr
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeStore) retractions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.retractIDs...)
}

func (f *fakeStore) CleanupHotSearch(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}

func (f *fakeStore) SearchHotSearch(context.Context, HotSearchOptions) (HotSearchResult, error) {
	return HotSearchResult{}, nil
}

func (f *fakeStore) Close() error { return nil }

func producerTestInput(id string) AuditLogInput {
	return AuditLogInput{
		ID:              id,
		LifecycleStatus: LifecycleFinalized,
		TraceID:         "trace-" + id,
		TrafficSource:   TrafficSourceGateway,
		AuditOutcome:    AuditOutcomeGatewaySucceeded,
		Method:          "POST",
		Path:            "/v1/chat/completions",
		Success:         true,
		SampleBucket:    0,
		SampleReason:    "always",
		StartedAt:       "2026-09-06T00:00:00Z",
		EndedAt:         "2026-09-06T00:00:01Z",
	}
}

func recordingProducerLogger(warns *[]string) producerLogger {
	return &fakeProducerLogger{warns: warns}
}

type fakeProducerLogger struct {
	mu    sync.Mutex
	warns *[]string
}

func (l *fakeProducerLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.warns = append(*l.warns, msg)
}

func (l *fakeProducerLogger) Error(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.warns = append(*l.warns, msg)
}

// snapshot returns a mutex-guarded copy: producer warns come from worker
// goroutines, so test assertions must not read the slice directly.
func (l *fakeProducerLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *l.warns...)
}

// TestProducerCapturePersistsAndAppendsHotSearch pins the happy path: renew →
// Persist → AppendHotSearch with the same input, exactly the retired input
// server write sequence minus HTTP.
func TestProducerCapturePersistsAndAppendsHotSearch(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("audit-1"))
	fake.waitForCalls(t, 3)

	renews, persists, hots := fake.counts()
	if renews != 1 || persists != 1 || hots != 1 {
		t.Fatalf("call counts renew=%d persist=%d hot=%d, want 1/1/1", renews, persists, hots)
	}
	stored := fake.persistedInputs()
	if len(stored) != 1 || stored[0].ID != "audit-1" {
		t.Fatalf("persisted inputs wrong: %#v", stored)
	}
}

// TestProducerCaptureSkipsHotSearchWhenIgnored pins the retired input-server
// branch: a persisted-but-ignored (late in_progress after finalized) input
// must not append to the hot-search mirror.
func TestProducerCaptureSkipsHotSearchWhenIgnored(t *testing.T) {
	fake := &fakeStore{renewOK: true, persistIgn: true}
	producer := NewProducer(fake, fixedLeaseSource{}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("audit-ignored"))
	fake.waitForCalls(t, 2)

	if _, persists, hots := fake.counts(); persists != 1 || hots != 0 {
		t.Fatalf("ignored input must persist once and skip hot search: persist=%d hot=%d", persists, hots)
	}
}

// TestProducerCaptureDropsOnLostOwnerLease pins the degradation contract: a
// rejected lease renewal drops the entry before Persist with a warn, never
// failing the caller (fire-and-forget) and never writing fenced.
func TestProducerCaptureDropsOnLostOwnerLease(t *testing.T) {
	for name, fake := range map[string]*fakeStore{
		"renew rejected": {renewOK: false},
		"renew error":    {renewErr: errors.New("storage down")},
	} {
		logger := &fakeProducerLogger{warns: &[]string{}}
		producer := NewProducer(fake, fixedLeaseSource{}, Config{OwnerLease: 30 * time.Second}, logger)
		producer.Capture(producerTestInput("audit-drop"))
		fake.waitForCalls(t, 1)

		if _, persists, hots := fake.counts(); persists != 0 || hots != 0 {
			t.Fatalf("%s: dropped capture must not persist or mirror: persist=%d hot=%d", name, persists, hots)
		}
		if len(logger.snapshot()) == 0 {
			t.Fatalf("%s: lease-loss drop must warn", name)
		}
	}
}

// TestProducerCapturePersistsWithoutRenewWhenTTLUnset pins the operationlog
// producer guard: a non-positive OwnerLease TTL would self-destruct the fence,
// so the renewal is skipped and the write proceeds under the shared keeper.
func TestProducerCapturePersistsWithoutRenewWhenTTLUnset(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{}, Config{}, nil)
	producer.Capture(producerTestInput("audit-no-ttl"))
	fake.waitForCalls(t, 2)

	if renews, _, _ := fake.counts(); renews != 0 {
		t.Fatalf("zero-TTL config must skip the per-record renewal, got %d", renews)
	}
}

// TestProducerCaptureSwallowsPersistAndHotSearchErrors pins the never-fail
// contract: persist failures and hot-search mirror failures are logged warns;
// the producer goroutine must not panic nor propagate.
func TestProducerCaptureSwallowsPersistAndHotSearchErrors(t *testing.T) {
	logger := &fakeProducerLogger{warns: &[]string{}}
	persistFake := &fakeStore{renewOK: true, persistErr: errors.New("persist failed")}
	NewProducer(persistFake, fixedLeaseSource{}, Config{OwnerLease: 30 * time.Second}, logger).Capture(producerTestInput("audit-persist-fail"))
	persistFake.waitForCalls(t, 2)
	if _, _, hots := persistFake.counts(); hots != 0 {
		t.Fatalf("failed persist must not reach hot search: hot=%d", hots)
	}

	hotFake := &fakeStore{renewOK: true, hotErr: errors.New("hot search failed")}
	NewProducer(hotFake, fixedLeaseSource{}, Config{OwnerLease: 30 * time.Second}, logger).Capture(producerTestInput("audit-hot-fail"))
	hotFake.waitForCalls(t, 3)

	if len(logger.snapshot()) < 2 {
		t.Fatalf("persist/hot failures must warn, got %v", logger.snapshot())
	}
}

// TestProducerCaptureConcurrentWrites pins that concurrent captures each run
// the full write path exactly once (the store mock is the serialization
// point; the producer must not lose or duplicate entries).
func TestProducerCaptureConcurrentWrites(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{}, Config{OwnerLease: 30 * time.Second}, nil)
	const writers = 32
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			producer.Capture(producerTestInput(string(rune('a'+i%26)) + "-audit"))
		}(index)
	}
	wg.Wait()
	fake.waitForCalls(t, writers*3)

	if renews, persists, hots := fake.counts(); renews != writers || persists != writers || hots != writers {
		t.Fatalf("concurrent counts renew=%d persist=%d hot=%d, want %d each", renews, persists, hots, writers)
	}
	if stored := fake.persistedInputs(); len(stored) != writers {
		t.Fatalf("persisted %d inputs, want %d", len(stored), writers)
	}
}

// gatedStore blocks the per-record lease renewal so tests can hold every
// worker inside the write path and fill the queue deterministically.
type gatedStore struct {
	*fakeStore
	gate    chan struct{}
	entered atomic.Int32
}

func (g *gatedStore) RenewOwnerLease(_ context.Context, _ OwnerLease, _ time.Duration) (bool, error) {
	g.entered.Add(1)
	<-g.gate
	return true, nil
}

// TestProducerCaptureDropsWhenQueueSaturated pins the bounded-queue contract:
// with every worker blocked in the store, the queue accepts exactly
// producerQueueCapacity records and a further capture is dropped with a warn
// carrying the running total — capture stays fire-and-forget on the caller.
func TestProducerCaptureDropsWhenQueueSaturated(t *testing.T) {
	inner := &fakeStore{renewOK: true}
	gate := make(chan struct{})
	store := &gatedStore{fakeStore: inner, gate: gate}
	logger := &fakeProducerLogger{warns: &[]string{}}
	producer := NewProducer(store, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, logger)

	// Park every worker inside the gated renewal so no further dequeues race
	// with the fills below.
	for i := 0; i < producerWorkers; i++ {
		producer.Capture(producerTestInput(fmt.Sprintf("inflight-%d", i)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() < producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != producerWorkers {
		t.Fatalf("only %d workers entered the gated renewal, want %d", store.entered.Load(), producerWorkers)
	}

	for i := 0; i < producerQueueCapacity; i++ {
		producer.Capture(producerTestInput(fmt.Sprintf("fill-%d", i)))
	}
	producer.Capture(producerTestInput("overflow-dropped"))
	if snapshot := logger.snapshot(); len(snapshot) == 0 || !strings.Contains(snapshot[len(snapshot)-1], "F3 审计采集队列已满") {
		t.Fatalf("saturated queue must warn on drop, got %v", snapshot)
	}
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("DroppedTotal = %d, want 1", dropped)
	}

	close(gate)
	// The overflow record was dropped, so only the parked + queued records
	// reach the store. Renewals run through the gatedStore override (counted
	// by `entered`), so the fake counters only see persist + hot search.
	want := producerWorkers + producerQueueCapacity
	deadline = time.Now().Add(10 * time.Second)
	for inner.countCalls() < want*2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	renews, persists, hots := inner.counts()
	if renews != 0 || persists != want || hots != want {
		t.Fatalf("store calls after drain: renew=%d persist=%d hot=%d, want 0/%d/%d", renews, persists, hots, want, want)
	}
	if entered := store.entered.Load(); entered != int32(want) {
		t.Fatalf("gate renewals = %d, want %d", entered, want)
	}
}

// TestProducerNilReceiverAndNilStoreAreInert pins the guard the dispatch
// adapters rely on: a nil producer (or a producer without a store) drops the
// capture silently instead of panicking on the request path.
func TestProducerNilReceiverAndNilStoreAreInert(t *testing.T) {
	var nilProducer *Producer
	nilProducer.Capture(producerTestInput("nil-receiver"))
	NewProducer(nil, fixedLeaseSource{}, Config{}, nil).Capture(producerTestInput("nil-store"))
}

// ---- W14 队列字节预算与可观测面 ----

// blockingStore 在 Release 前让每次 Persist 挂起（慢存储注入）：租约续约照常
// 成功，Persist 阻塞在信号上，用于锁定「慢存储下队列字节驻留有界」。
type blockingStore struct {
	fakeStore
	release chan struct{}
}

func (f *blockingStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	<-f.release
	f.mu.Lock()
	f.persistCalls++
	f.persisted = append(f.persisted, input)
	f.mu.Unlock()
	return PersistResult{}, nil
}

// TestProducerCaptureBoundsQueueBytesUnderSlowStore 锁定 W14 核心契约：
// 慢存储挂住全部 worker 时，字节预算（而非条数上限）决定准入——
// 预算满后继续采集只丢弃不阻塞；驻留 gauge 等于预算上界；释放后台账归零、
// 已准入条目全部落库。正常小记录不因预算误丢（预算按估算字节精确放开）。
func TestProducerCaptureBoundsQueueBytesUnderSlowStore(t *testing.T) {
	previousBudget := producerQueueMaxBytes
	input := producerTestInput("w14-bound")
	input.Payloads = []AuditLogPayloadInput{{ID: "p1", PartType: PayloadPartUpstreamRequest, Body: PayloadBody{Bytes: []byte("0123456789abcdef"), Present: true}}}

	store := &blockingStore{fakeStore: fakeStore{renewOK: true}, release: make(chan struct{})}
	producer := NewProducer(store, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	const total = 10
	items := make([]AuditLogInput, total)
	estimate := 0
	for index := 0; index < total; index++ {
		items[index] = input
		items[index].ID = input.ID + "-" + strconv.Itoa(index)
		items[index].TraceID = "trace-" + items[index].ID
		estimate = estimateAuditInputBytes(items[index])
	}
	// 预算恰好容纳 3 条：第 4 条起必须因字节预算丢弃（而非条数上限）。
	producerQueueMaxBytes = int64(3 * estimate)
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	for index := 0; index < total; index++ {
		producer.Capture(items[index])
	}

	if dropped := producer.DroppedTotal(); dropped != total-3 {
		t.Fatalf("预算 3 条时应丢弃 %d 条，实际 %d", total-3, dropped)
	}
	if got := producer.QueueBytes(); got != int64(3*estimate) {
		t.Fatalf("驻留 gauge 必须钉在预算上界 %d，实际 %d", 3*estimate, got)
	}
	if producer.QueueDepth() > 3 {
		t.Fatalf("队列深度不得超过准入数 3，实际 %d", producer.QueueDepth())
	}
	close(store.release)
	store.waitForCalls(t, 3)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && producer.QueueBytes() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := producer.QueueBytes(); got != 0 {
		t.Fatalf("释放后台账必须归零，实际 %d", got)
	}
	if persisted := store.persistedInputs(); len(persisted) != 3 {
		t.Fatalf("已准入条目必须全部落库，实际 %d", len(persisted))
	}
}

// TestProducerCaptureDropsOversizeInput 锁定单条超预算臂：单条估算超出
// 全局预算时立即丢弃（不占队列），warn 与丢弃计数可观测。
func TestProducerCaptureDropsOversizeInput(t *testing.T) {
	previousBudget := producerQueueMaxBytes
	producerQueueMaxBytes = 1024
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	fake := &fakeStore{renewOK: true}
	logger := &fakeProducerLogger{warns: &[]string{}}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, logger)
	oversize := producerTestInput("w14-oversize")
	oversize.Payloads = []AuditLogPayloadInput{{ID: "p1", PartType: PayloadPartUpstreamRequest, Body: PayloadBody{Bytes: make([]byte, 4096), Present: true}}}
	producer.Capture(oversize)

	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("单条超预算必须丢弃一次，实际 %d", dropped)
	}
	if got := producer.QueueBytes(); got != 0 {
		t.Fatalf("超预算条目不得占预算，实际 %d", got)
	}
	if _, persists, _ := fake.counts(); persists != 0 {
		t.Fatalf("超预算条目不得进入持久化，实际 %d", persists)
	}
	found := false
	for _, warn := range logger.snapshot() {
		if strings.Contains(warn, "单条超出队列字节预算") {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须记录单条超预算告警: %v", logger.snapshot())
	}
}

// TestProducerPersistDurationObserver 锁定时延观察者契约：成功落库上报
// "ok"，持久化失败上报 "error"，未接线观察者时静默。
func TestProducerPersistDurationObserver(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	var mu sync.Mutex
	var results []string
	producer.SetPersistDurationObserver(func(result string, durationMS int64) {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, result)
	})
	producer.Capture(producerTestInput("w14-ok"))
	fake.waitForCalls(t, 3)
	failing := &fakeStore{renewOK: true, persistErr: errors.New("persist down")}
	failingProducer := NewProducer(failing, fixedLeaseSource{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
	failingProducer.SetPersistDurationObserver(func(result string, durationMS int64) {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, result)
	})
	failingProducer.Capture(producerTestInput("w14-error"))
	failing.waitForCalls(t, 2)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(results) >= 2
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 2 || results[0] != "ok" || results[1] != "error" {
		t.Fatalf("时延观察结果 = %v, want [ok error]", results)
	}
}

// TestEstimateAuditInputBytesDominatesOnBodies 锁定估算口径：payload 正文
// 按精确字节计（驻留主体），错误文本计入，空记录有固定基线。
func TestEstimateAuditInputBytesDominatesOnBodies(t *testing.T) {
	baseline := estimateAuditInputBytes(producerTestInput("est"))
	withBody := producerTestInput("est")
	withBody.Payloads = []AuditLogPayloadInput{{ID: "p1", PartType: PayloadPartUpstreamRequest, Body: PayloadBody{Bytes: make([]byte, 10_000), Present: true}}}
	if got := estimateAuditInputBytes(withBody); got < baseline+10_000 {
		t.Fatalf("正文必须按精确字节计入: baseline=%d got=%d", baseline, got)
	}
	withError := producerTestInput("est")
	withError.ErrorMessage = strings.Repeat("e", 500)
	if got := estimateAuditInputBytes(withError); got < baseline+500 {
		t.Fatalf("错误文本必须计入: baseline=%d got=%d", baseline, got)
	}
}
