package inval

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// TestRedisSharedStoreCrossInstanceSync wires two buses on one miniredis (the
// two Go gateway instances) and replays the production sync flow: instance A
// publishes, instance B's cache-miss sync observes the bump, B publishes and
// A's sync sees it. The stored version never moves backwards even when a
// fresh instance proposes a lower counter.
func TestRedisSharedStoreCrossInstanceSync(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisSharedStore(client, "juhe-ai:dev")
	clock := newFakeClock(time.Unix(1000, 0))
	busA := New(clock.Now)
	busA.SetSharedStore(store)
	busB := New(clock.Now)
	busB.SetSharedStore(NewRedisSharedStore(redis.NewClient(&redis.Options{Addr: server.Addr()}), "juhe-ai:dev"))

	// A publishes twice (its local counter reaches 2 in the shared store).
	busA.Invalidate(TopicGatewayRuntime, "account_management_patch")
	clock.advance(2 * time.Second) // leave the 1s coalesce window
	busA.Invalidate(TopicGatewayRuntime, "account_deleted")
	if got := busA.Version(TopicGatewayRuntime); got != 2 {
		t.Fatalf("A version = %d, want 2", got)
	}

	// B syncs on cache miss and adopts A's version.
	if err := busB.SyncFromShared(context.Background(), TopicGatewayRuntime); err != nil {
		t.Fatal(err)
	}
	if got := busB.Version(TopicGatewayRuntime); got != 2 {
		t.Fatalf("B version = %d, want shared 2", got)
	}

	// B publishes: monotonic max keeps 3 (B's local 2 + proposal 3).
	busB.Invalidate(TopicGatewayRuntime, "oauth_credentials_rotated")
	store2 := NewRedisSharedStore(client, "juhe-ai:dev")
	if got, err := store2.GetVersion(context.Background(), TopicGatewayRuntime); err != nil || got != 3 {
		t.Fatalf("shared version = %d, %v; want 3", got, err)
	}

	// A syncs back and sees B's bump.
	if err := busA.SyncFromShared(context.Background(), TopicGatewayRuntime); err != nil {
		t.Fatal(err)
	}
	if got := busA.Version(TopicGatewayRuntime); got != 3 {
		t.Fatalf("A version after resync = %d, want 3", got)
	}
}

// TestRedisSharedStoreMonotonicAgainstStaleProposal proves the Lua max() CAS:
// a stale proposal below the stored version is dropped and the effective
// version returns so the caller can adopt it.
func TestRedisSharedStoreMonotonicAgainstStaleProposal(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisSharedStore(client, "")
	ctx := context.Background()
	effective, err := store.PublishVersion(ctx, TopicAPIKeyQuota, 7)
	if err != nil || effective != 7 {
		t.Fatalf("first publish = %d, %v; want 7", effective, err)
	}
	effective, err = store.PublishVersion(ctx, TopicAPIKeyQuota, 3)
	if err != nil || effective != 7 {
		t.Fatalf("stale publish = %d, %v; want monotonic 7", effective, err)
	}
	// The namespaced key layout is the Go-only protocol
	// (juhe-ai:inval:topic-version:<topic> when the namespace is empty).
	if got, err := store.GetVersion(ctx, TopicAPIKeyQuota); err != nil || got != 7 {
		t.Fatalf("stored version = %d, %v; want 7", got, err)
	}
	if _, err := client.Get(ctx, "juhe-ai:inval:topic-version:"+TopicAPIKeyQuota).Int64(); err != nil {
		t.Fatalf("expected Go-only key layout: %v", err)
	}
}
