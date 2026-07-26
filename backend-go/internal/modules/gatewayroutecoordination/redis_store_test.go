package gatewayroutecoordination

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestRedisStoreSharesRoundRobinAndRefreshesState(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	first, firstClient, closeFirst := newRedisStore(t, server, "prod")
	defer closeFirst()
	second, _, closeSecond := newRedisStore(t, server, "prod")
	defer closeSecond()
	snapshot := testSnapshot(gatewayrouting.ModeRoundRobin, 1, 1)

	plan, err := first.Plan(t.Context(), snapshot)
	if err != nil || !reflect.DeepEqual(bindingIDs(plan.Ordered), []string{"a", "b"}) {
		t.Fatalf("first Plan() = %#v, %v", plan, err)
	}
	plan, err = second.Plan(t.Context(), snapshot)
	if err != nil || !reflect.DeepEqual(bindingIDs(plan.Ordered), []string{"b", "a"}) {
		t.Fatalf("second Plan() = %#v, %v", plan, err)
	}
	key, err := redisStateKey(snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL(firstClient.Key(key)); ttl <= 0 || ttl > RedisStateTTL {
		t.Fatalf("state ttl = %s, want (0, %s]", ttl, RedisStateTTL)
	}
}

func TestRedisStoreResetsRevisionAndSkipsPassThroughRedis(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	store, client, closeStore := newRedisStore(t, server, "revision")
	defer closeStore()
	snapshot := testSnapshot(gatewayrouting.ModeRoundRobin, 1, 1)
	if _, err := store.Plan(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	changed := snapshot
	changed.Bindings[0].Weight = 2
	plan, err := store.Plan(t.Context(), changed)
	if err != nil || !reflect.DeepEqual(bindingIDs(plan.Ordered), []string{"a", "b"}) {
		t.Fatalf("revision reset Plan() = %#v, %v", plan, err)
	}
	key, err := redisStateKey(snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	normal := changed
	normal.Mode = gatewayrouting.ModeNormal
	plan, err = store.Plan(t.Context(), normal)
	if err != nil || plan.StateAdvanced || server.Exists(client.Key(key)) {
		t.Fatalf("normal Plan() = %#v, %v; stale redis state exists=%v", plan, err, server.Exists(client.Key(key)))
	}

	called := 0
	passThrough := &RedisStore{
		get: func(context.Context, string) ([]byte, error) {
			called++
			return nil, redisplatform.ErrNotFound
		},
		compareAndSwap: func(context.Context, string, []byte, []byte, time.Duration) (bool, error) {
			called++
			return false, errors.New("must not write Redis")
		},
		ttl:           RedisStateTTL,
		maxCASRetries: RedisStateMaxCASRetries,
	}
	passThroughSnapshot := testSnapshot(gatewayrouting.ModeNormal, 1, 1)
	plan, err = passThrough.Plan(t.Context(), passThroughSnapshot)
	if err != nil || plan.StateAdvanced || called != 1 {
		t.Fatalf("pass-through Plan() = %#v, %v; redis calls=%d", plan, err, called)
	}
}

func TestRedisStoreRejectsUnknownAndStaleDispatchGeneration(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	store, _, closeStore := newRedisStore(t, server, "generation")
	defer closeStore()
	snapshot := testSnapshot(gatewayrouting.ModeRoundRobin, 1, 1)
	snapshot.DispatchGeneration = 0
	if _, err := store.Plan(t.Context(), snapshot); err == nil {
		t.Fatal("Plan() accepted unknown dispatch generation")
	}
	snapshot.DispatchGeneration = 2
	if _, err := store.Plan(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	stale := snapshot
	stale.DispatchGeneration = 1
	if _, err := store.Plan(t.Context(), stale); !errors.Is(err, ErrStaleDispatchGeneration) {
		t.Fatalf("Plan() error = %v, want stale dispatch generation", err)
	}
	staleNormal := stale
	staleNormal.Mode = gatewayrouting.ModeNormal
	if _, err := store.Plan(t.Context(), staleNormal); !errors.Is(err, ErrStaleDispatchGeneration) {
		t.Fatalf("Plan() stale normal error = %v, want stale dispatch generation", err)
	}
}

func TestRedisStoreWeightedStateIsSharedAndConcurrencySafe(t *testing.T) {
	server := miniredis.RunT(t)
	store, _, closeStore := newRedisStore(t, server, "weighted")
	defer closeStore()
	snapshot := testSnapshot(gatewayrouting.ModeWeighted, 3, 1)
	const requests = 40
	selected := make(chan string, requests)
	errorsCh := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			plan, err := store.Plan(t.Context(), snapshot)
			if err != nil {
				errorsCh <- err
				return
			}
			selected <- plan.Ordered[0].ID
		}()
	}
	group.Wait()
	close(selected)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("Plan() error = %v", err)
	}
	counts := map[string]int{}
	for id := range selected {
		counts[id]++
	}
	if want := map[string]int{"a": 30, "b": 10}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("weighted selection counts = %#v, want %#v", counts, want)
	}
}

func TestRedisStoreRejectsContentionAndMalformedState(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot(gatewayrouting.ModeWeighted, 3, 1)
	calls := 0
	store := &RedisStore{
		get: func(context.Context, string) ([]byte, error) { return nil, redisplatform.ErrNotFound },
		compareAndSwap: func(context.Context, string, []byte, []byte, time.Duration) (bool, error) {
			calls++
			return false, nil
		},
		ttl:           RedisStateTTL,
		maxCASRetries: 2,
	}
	if _, err := store.Plan(t.Context(), snapshot); err == nil || calls != 2 {
		t.Fatalf("contention Plan() error = %v, calls=%d", err, calls)
	}
	malformed := &RedisStore{
		get: func(context.Context, string) ([]byte, error) { return []byte("not-json"), nil },
		compareAndSwap: func(context.Context, string, []byte, []byte, time.Duration) (bool, error) {
			return true, nil
		},
		ttl:           RedisStateTTL,
		maxCASRetries: RedisStateMaxCASRetries,
	}
	if _, err := malformed.Plan(t.Context(), snapshot); err == nil {
		t.Fatal("malformed state Plan() error = nil")
	}
	for _, raw := range [][]byte{
		[]byte(`{"v":2,"g":1,"r":"revision","s":0}`),
		[]byte(`{"v":1,"g":1,"r":"revision","s":0,"extra":true}`),
		[]byte(`{"v":1,"g":1,"r":"revision","s":-1}`),
		[]byte(`{"v":1,"g":1,"r":"revision","s":0} trailing`),
		[]byte(`{"v":1,"r":"revision","s":0}`),
	} {
		if _, err := decodeRedisState(raw); err == nil {
			t.Fatalf("decodeRedisState(%q) error = nil", raw)
		}
	}
}

func newRedisStore(t *testing.T, server *miniredis.Miniredis, namespace string) (*RedisStore, *redisplatform.Client, func()) {
	t.Helper()
	client, err := redisplatform.NewClient("redis://"+server.Addr()+"/0", namespace+":state")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	store, err := NewRedisStore(client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	return store, client, func() { _ = client.Close() }
}
