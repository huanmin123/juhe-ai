package inval

import (
	"sync/atomic"
	"context"
	"sync"
	"testing"
	"time"
)

func TestInvalidateBumpsVersionAndNotifies(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	bus := New(clock.Now)
	calls := 0
	bus.Subscribe(TopicGatewayRuntime, func(topic, reason string) {
		calls++
		if topic != TopicGatewayRuntime {
			t.Fatalf("topic = %s", topic)
		}
		if reason != "settings_updated" {
			t.Fatalf("reason = %s", reason)
		}
	})

	bus.Invalidate(TopicGatewayRuntime, "settings_updated")
	if calls != 1 {
		t.Fatalf("first invalidation must notify, calls=%d", calls)
	}
	clock.advance(2 * time.Second) // past the 1s coalesce window
	bus.Invalidate(TopicGatewayRuntime, "settings_updated")
	if calls != 2 {
		t.Fatalf("second invalidation must notify, calls=%d", calls)
	}
	if bus.Version(TopicGatewayRuntime) != 2 {
		t.Fatalf("version = %d, want 2", bus.Version(TopicGatewayRuntime))
	}
}

func TestThrottleCoalescesWithinOneSecond(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	bus := New(clock.Now)
	calls := 0
	bus.Subscribe(TopicAuthorizationQuota, func(topic, reason string) { calls++ })

	bus.Invalidate(TopicAuthorizationQuota, "a")
	clock.advance(500 * time.Millisecond)
	bus.Invalidate(TopicAuthorizationQuota, "b") // coalesced away
	if calls != 1 {
		t.Fatalf("coalesced invalidation leaked: %d calls", calls)
	}

	clock.advance(2 * time.Second)
	bus.Invalidate(TopicAuthorizationQuota, "c")
	if calls != 2 {
		t.Fatalf("post-throttle invalidation missing: %d calls", calls)
	}
}

func TestSyncFromSharedTakesMax(t *testing.T) {
	bus := New(nil)
	bus.versions[TopicAPIKeyQuota] = 5
	bus.SetSharedStore(&fakeShared{versions: map[string]int64{
		TopicAPIKeyQuota:    9,
		TopicGatewayRuntime: 0,
	}})

	if err := bus.SyncFromShared(context.Background(), TopicAPIKeyQuota, TopicGatewayRuntime); err != nil {
		t.Fatal(err)
	}
	if bus.Version(TopicAPIKeyQuota) != 9 {
		t.Fatalf("version = %d, want shared max 9", bus.Version(TopicAPIKeyQuota))
	}
	if bus.Version(TopicGatewayRuntime) != 0 {
		t.Fatalf("missing topic must stay 0, got %d", bus.Version(TopicGatewayRuntime))
	}
}

// TestInvalidateAdoptsSharedVersionOnPublish covers the monotonic cross-instance
// contract: a fresh instance proposing 1 while the cluster sits at 9 must not
// drag the shared version backwards — it adopts 9 and its next proposal orders
// above (10).
func TestInvalidateAdoptsSharedVersionOnPublish(t *testing.T) {
	shared := &fakeShared{versions: map[string]int64{TopicGatewayRuntime: 9}}
	clock := newFakeClock(time.Unix(1000, 0))
	bus := New(clock.Now)
	bus.SetSharedStore(shared)

	bus.Invalidate(TopicGatewayRuntime, "account_batch_updated")
	if got := shared.versions[TopicGatewayRuntime]; got != 9 {
		t.Fatalf("shared version = %d, want monotonic 9", got)
	}
	if bus.Version(TopicGatewayRuntime) != 9 {
		t.Fatalf("local version = %d, want adopted 9", bus.Version(TopicGatewayRuntime))
	}

	clock.advance(2 * time.Second) // leave the 1s coalesce window
	bus.Invalidate(TopicGatewayRuntime, "account_batch_updated")
	if got := shared.versions[TopicGatewayRuntime]; got != 10 {
		t.Fatalf("second proposal = %d, want 10 (adopted 9 + 1)", got)
	}
}

// TestTwoBusesObserveEachOtherThroughSharedStore replays the two-instance
// flow: A invalidates, B syncs on read and sees the bump, B invalidates and A
// syncs back.
func TestTwoBusesObserveEachOtherThroughSharedStore(t *testing.T) {
	shared := &fakeShared{versions: map[string]int64{}}
	clock := newFakeClock(time.Unix(2000, 0))
	busA := New(clock.Now)
	busA.SetSharedStore(shared)
	busB := New(clock.Now)
	busB.SetSharedStore(shared)

	busA.Invalidate(TopicGatewayAPIKeyValidation, "api_key_updated acc-1")
	if err := busB.SyncFromShared(context.Background(), TopicGatewayAPIKeyValidation); err != nil {
		t.Fatal(err)
	}
	if busB.Version(TopicGatewayAPIKeyValidation) != busA.Version(TopicGatewayAPIKeyValidation) {
		t.Fatalf("B = %d, A = %d: sync must adopt the shared version",
			busB.Version(TopicGatewayAPIKeyValidation), busA.Version(TopicGatewayAPIKeyValidation))
	}

	clock.advance(2 * time.Second) // B publishes outside A's coalesce window
	busB.Invalidate(TopicGatewayAPIKeyValidation, "api_key_deleted acc-2")
	if err := busA.SyncFromShared(context.Background(), TopicGatewayAPIKeyValidation); err != nil {
		t.Fatal(err)
	}
	if busA.Version(TopicGatewayAPIKeyValidation) != 2 {
		t.Fatalf("A version = %d, want 2 after B's bump", busA.Version(TopicGatewayAPIKeyValidation))
	}
}

// TestConcurrentPublishStaysMonotonic hammers one shared store from many
// buses past the coalesce window: every adopted local version and the stored
// version stay monotonic, and each round lifts the store by at least one.
func TestConcurrentPublishStaysMonotonic(t *testing.T) {
	shared := &fakeShared{versions: map[string]int64{}}
	clock := newFakeClock(time.Unix(3000, 0))
	var observed atomic.Int64
	note := func(version int64) {
		for {
			previous := observed.Load()
			if version >= previous {
				if observed.CompareAndSwap(previous, version) {
					return
				}
				continue
			}
			t.Errorf("shared version moved backwards: %d after %d", version, previous)
			return
		}
	}
	var wg sync.WaitGroup
	rounds := 25
	// One observed store shared by every bus: the wrapper lock keeps the
	// observations in store-commit order (mirrors one Redis in production).
	observedStore := &observingShared{inner: shared, onPublish: note}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus := New(clock.Now)
			bus.SetSharedStore(observedStore)
			for j := 0; j < rounds; j++ {
				bus.Invalidate(TopicAuthorizationQuota, "quota_sync")
				clock.advance(2 * time.Second)
			}
		}()
	}
	wg.Wait()
	if got := shared.snapshot(TopicAuthorizationQuota); got < int64(rounds) {
		t.Fatalf("shared version = %d, want monotonic accumulation >= %d", got, rounds)
	}
}

type fakeClock struct {
	now atomic.Int64 // unix nanoseconds
}

func newFakeClock(start time.Time) *fakeClock {
	clock := &fakeClock{}
	clock.now.Store(start.UnixNano())
	return clock
}

func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.now.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.now.Store(c.now.Load() + int64(d)) }

// fakeShared implements SharedStore with the same max() contract as the Redis
// store so the bus-level tests replay the production monotonicity.
type fakeShared struct {
	mu       sync.Mutex
	versions map[string]int64
}

func (f *fakeShared) GetVersion(_ context.Context, topic string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions[topic], nil
}

func (f *fakeShared) PublishVersion(_ context.Context, topic string, version int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version > f.versions[topic] {
		f.versions[topic] = version
	}
	return f.versions[topic], nil
}

func (f *fakeShared) snapshot(topic string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions[topic]
}

// observingShared wraps a shared store and records every effective version so
// the monotonicity race test can observe the published sequence. The wrapper
// lock keeps the observation in store-commit order (two concurrent publishers
// may otherwise report the same effective version out of turn).
type observingShared struct {
	mu        sync.Mutex
	inner     *fakeShared
	onPublish func(version int64)
}

func (o *observingShared) GetVersion(ctx context.Context, topic string) (int64, error) {
	return o.inner.GetVersion(ctx, topic)
}

func (o *observingShared) PublishVersion(ctx context.Context, topic string, version int64) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	effective, err := o.inner.PublishVersion(ctx, topic, version)
	if err == nil {
		o.onPublish(effective)
	}
	return effective, err
}
