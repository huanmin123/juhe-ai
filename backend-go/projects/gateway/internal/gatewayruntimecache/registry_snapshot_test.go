package gatewayruntimecache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// internal gateway registry（注册/注销/枚举，miniredis 闭环）
// ---------------------------------------------------------------------------

func TestRegistryPublishListUnregister(t *testing.T) {
	mr := miniredis.RunT(t)
	redisURL := "redis://" + mr.Addr()

	publisherConfig := RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "registry-secret",
		InstanceID: "gw-1", Port: 65432,
		PublisherEnabled: true,
	}
	publisher, err := NewRegistry(publisherConfig)
	if err != nil {
		t.Fatalf("NewRegistry(publisher): %v", err)
	}
	defer publisher.Close()
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "registry-secret",
		InstanceID: "gw-1", Port: 65432,
		ReaderEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewRegistry(reader): %v", err)
	}
	defer reader.Close()

	// 注册前为空。
	endpoints, err := reader.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("registry must start empty, got %v", endpoints)
	}

	publisher.Start()
	ctx := context.Background()
	waitFor(t, 5*time.Second, func() bool {
		endpoints, err = reader.ListEndpoints(ctx)
		if err != nil {
			t.Fatalf("list after publish: %v", err)
		}
		return len(endpoints) == 1
	})
	if endpoints[0].InstanceID != "gw-1" {
		t.Fatalf("instance id mismatch: %+v", endpoints[0])
	}
	if endpoints[0].Origin != "http://127.0.0.1:65432" {
		t.Fatalf("origin mismatch: %+v", endpoints[0])
	}

	// 注销：Stop 后心跳停止、条目删除。
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := publisher.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		endpoints, err = reader.ListEndpoints(ctx)
		if err != nil {
			t.Fatalf("list after stop: %v", err)
		}
		return len(endpoints) == 0
	})
}

func TestRegistrySignatureValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	redisURL := "redis://" + mr.Addr()
	publisher, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "secret-a",
		InstanceID: "gw-sig", Port: 65433, PublisherEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "secret-b",
		InstanceID: "gw-sig", Port: 65433, ReaderEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	// 不同 secret：签名校验必须拒绝该条目。
	publisher.Start()
	time.Sleep(300 * time.Millisecond)
	endpoints, err := reader.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("list with mismatched secret: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("signature mismatch must reject the entry, got %v", endpoints)
	}
	_ = publisher.Stop(context.Background())
}

func TestRegistryReaderDisabledReturnsEmpty(t *testing.T) {
	registry, err := NewRegistry(RegistryConfig{
		RedisURL: "redis://127.0.0.1:1", Namespace: "dev", Secret: "s",
		InstanceID: "gw", Port: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	endpoints, err := registry.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("disabled reader: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("disabled reader must return empty, got %v", endpoints)
	}
}

// ---------------------------------------------------------------------------
// runtime snapshot 缓存（300ms TTL / 5s max-stale / 100ms 最小刷新间隔）
// ---------------------------------------------------------------------------

func TestRuntimeSnapshotServerCacheTTLAndStale(t *testing.T) {
	clock := newManualClock()
	var calls atomic.Int32
	svc := NewRuntimeSnapshotService(clock, nil, nil, func(ctx context.Context) (*AccountRuntimeSnapshot, error) {
		calls.Add(1)
		return &AccountRuntimeSnapshot{
			AccountConcurrency:         AccountConcurrencySnapshot{"a1": 2},
			AccountRuntimeAvailability: AccountRuntimeAvailabilitySnapshot{"a1": []byte(`{"status":"active"}`)},
		}, nil
	})
	ctx := context.Background()

	// 首载。
	concurrency, ok := svc.LoadAccountConcurrencyByIDs(ctx, []string{"a1", "a1", ""})
	if !ok || concurrency["a1"] != 2 {
		t.Fatalf("first load mismatch: %v %v", ok, concurrency)
	}
	if calls.Load() != 1 {
		t.Fatalf("first load must hit the loader once, got %d", calls.Load())
	}
	// 300ms 内命中。
	if _, ok := svc.LoadAccountConcurrencyByIDs(ctx, []string{"a1"}); !ok {
		t.Fatal("fresh snapshot must answer")
	}
	if calls.Load() != 1 {
		t.Fatalf("fresh snapshot must not reload, got %d calls", calls.Load())
	}
	// 400ms：超过 TTL 但在 5s max-stale 内 → 返回旧值 + 后台刷新。
	clock.Advance(400 * time.Millisecond)
	if _, ok := svc.LoadServerAccountRuntimeAvailabilitySnapshot(ctx); !ok {
		t.Fatal("stale snapshot must answer")
	}
	if calls.Load() < 1 {
		t.Fatal("loader must have run at least once")
	}
	// 5s 后：max-stale 窗口外 → 同步刷新。
	clock.Advance(6 * time.Second)
	before := calls.Load()
	if _, ok := svc.LoadAccountConcurrencyByIDs(ctx, []string{"a1"}); !ok {
		t.Fatal("post-max-stale load must answer")
	}
	if calls.Load() <= before {
		t.Fatalf("post-max-stale load must refresh synchronously, before=%d after=%d", before, calls.Load())
	}
}

func TestRuntimeSnapshotUnavailableSemantics(t *testing.T) {
	clock := newManualClock()
	// 无任何 loader：available=false。
	empty := NewRuntimeSnapshotService(clock, nil, nil, nil)
	if _, ok := empty.LoadAccountConcurrencyByIDs(context.Background(), []string{"a1"}); ok {
		t.Fatal("without loaders the snapshot must be unavailable")
	}
	// loader 失败：available=false，不毒化。
	failing := NewRuntimeSnapshotService(clock, nil, nil, func(ctx context.Context) (*AccountRuntimeSnapshot, error) {
		return nil, errors.New("db down")
	})
	if _, ok := failing.LoadAccountConcurrencyByIDs(context.Background(), []string{"a1"}); ok {
		t.Fatal("failing loader must report unavailable")
	}
	availability, ok := failing.LoadAccountRuntimeAvailabilityByKeys(context.Background(), []string{"a1"})
	if ok || len(availability) != 0 {
		t.Fatal("failing loader must report unavailable availability")
	}
}

func TestRuntimeSnapshotRedisLoaderTakesPrecedence(t *testing.T) {
	clock := newManualClock()
	serverCalls := 0
	svc := NewRuntimeSnapshotService(clock,
		func(ctx context.Context, keys []string) (AccountRuntimeAvailabilitySnapshot, bool, error) {
			return AccountRuntimeAvailabilitySnapshot{"a1": []byte(`{"x":1}`)}, true, nil
		},
		func(ctx context.Context, ids []string) (AccountConcurrencySnapshot, bool, error) {
			return AccountConcurrencySnapshot{"a1": 7}, true, nil
		},
		func(ctx context.Context) (*AccountRuntimeSnapshot, error) {
			serverCalls++
			return &AccountRuntimeSnapshot{}, nil
		})
	ctx := context.Background()
	concurrency, ok := svc.LoadAccountConcurrencyByIDs(ctx, []string{"a1"})
	if !ok || concurrency["a1"] != 7 {
		t.Fatalf("redis concurrency overlay mismatch: %v %v", ok, concurrency)
	}
	availability, ok := svc.LoadAccountRuntimeAvailabilityByKeys(ctx, []string{"a1", "a2"})
	if !ok || len(availability) != 1 {
		t.Fatalf("redis availability overlay mismatch: %v %v", ok, availability)
	}
	if serverCalls != 0 {
		t.Fatalf("redis loaders must bypass the server snapshot, server calls = %d", serverCalls)
	}
}
