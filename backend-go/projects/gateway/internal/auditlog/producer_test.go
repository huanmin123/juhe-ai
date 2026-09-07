package auditlog

import (
	"context"
	"errors"
	"sync"
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

	renewOK    bool
	renewErr   error
	persistErr error
	persistIgn bool
	hotErr     error

	persisted []AuditLogInput
	hotInputs [][]AuditLogInput

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
	return f.renewCalls + f.persistCalls + f.hotCalls
}

func (f *fakeStore) EnsureSchema(context.Context) error { return nil }

func (f *fakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, true, nil
}

func (f *fakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
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
	warns *[]string
}

func (l *fakeProducerLogger) Warn(msg string, _ ...any) {
	*l.warns = append(*l.warns, msg)
}

func (l *fakeProducerLogger) Error(msg string, _ ...any) {
	*l.warns = append(*l.warns, msg)
}

// TestProducerCapturePersistsAndAppendsHotSearch pins the happy path: renew →
// Persist → AppendHotSearch with the same input, exactly the retired input
// server write sequence minus HTTP.
func TestProducerCapturePersistsAndAppendsHotSearch(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, OwnerLease{OwnerID: "owner-1", FenceToken: 7}, Config{OwnerLease: 30 * time.Second}, nil)
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
	producer := NewProducer(fake, OwnerLease{}, Config{OwnerLease: 30 * time.Second}, nil)
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
		var warns []string
		producer := NewProducer(fake, OwnerLease{}, Config{OwnerLease: 30 * time.Second}, recordingProducerLogger(&warns))
		producer.Capture(producerTestInput("audit-drop"))
		fake.waitForCalls(t, 1)

		if _, persists, hots := fake.counts(); persists != 0 || hots != 0 {
			t.Fatalf("%s: dropped capture must not persist or mirror: persist=%d hot=%d", name, persists, hots)
		}
		if len(warns) == 0 {
			t.Fatalf("%s: lease-loss drop must warn", name)
		}
	}
}

// TestProducerCapturePersistsWithoutRenewWhenTTLUnset pins the operationlog
// producer guard: a non-positive OwnerLease TTL would self-destruct the fence,
// so the renewal is skipped and the write proceeds under the shared keeper.
func TestProducerCapturePersistsWithoutRenewWhenTTLUnset(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, OwnerLease{}, Config{}, nil)
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
	var warns []string
	persistFake := &fakeStore{renewOK: true, persistErr: errors.New("persist failed")}
	NewProducer(persistFake, OwnerLease{}, Config{OwnerLease: 30 * time.Second}, recordingProducerLogger(&warns)).Capture(producerTestInput("audit-persist-fail"))
	persistFake.waitForCalls(t, 2)
	if _, _, hots := persistFake.counts(); hots != 0 {
		t.Fatalf("failed persist must not reach hot search: hot=%d", hots)
	}

	hotFake := &fakeStore{renewOK: true, hotErr: errors.New("hot search failed")}
	NewProducer(hotFake, OwnerLease{}, Config{OwnerLease: 30 * time.Second}, recordingProducerLogger(&warns)).Capture(producerTestInput("audit-hot-fail"))
	hotFake.waitForCalls(t, 3)

	if len(warns) < 2 {
		t.Fatalf("persist/hot failures must warn, got %v", warns)
	}
}

// TestProducerCaptureConcurrentWrites pins that concurrent captures each run
// the full write path exactly once (the store mock is the serialization
// point; the producer must not lose or duplicate entries).
func TestProducerCaptureConcurrentWrites(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, OwnerLease{}, Config{OwnerLease: 30 * time.Second}, nil)
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

// TestProducerNilReceiverAndNilStoreAreInert pins the guard the dispatch
// adapters rely on: a nil producer (or a producer without a store) drops the
// capture silently instead of panicking on the request path.
func TestProducerNilReceiverAndNilStoreAreInert(t *testing.T) {
	var nilProducer *Producer
	nilProducer.Capture(producerTestInput("nil-receiver"))
	NewProducer(nil, OwnerLease{}, Config{}, nil).Capture(producerTestInput("nil-store"))
}
