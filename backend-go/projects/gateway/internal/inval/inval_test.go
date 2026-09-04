package inval

import (
	"context"
	"testing"
	"time"
)

func TestInvalidateBumpsVersionAndNotifies(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
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
	clock := &fakeClock{now: time.Unix(1000, 0)}
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

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type fakeShared struct {
	versions map[string]int64
}

func (f *fakeShared) GetVersion(_ context.Context, topic string) (int64, error) {
	return f.versions[topic], nil
}

func (f *fakeShared) SetVersion(_ context.Context, topic string, version int64) error {
	f.versions[topic] = version
	return nil
}
