package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func w1iSetupRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(func() { mr.Close() })
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func w1iRedisStore(t *testing.T, namespace string) (*chainTurnRetryRedisStateStore, *redis.Client) {
	t.Helper()
	client := w1iSetupRedis(t)
	store, err := newChainTurnRetryRedisStateStore(client, namespace)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store, client
}

func TestW1iChainTurnRetryKeyPrefixValid(t *testing.T) {
	for _, ns := range []string{"test", "prod"} {
		prefix, err := chainTurnRetryKeyPrefix(ns)
		if err != nil {
			t.Fatalf("ns %q: %v", ns, err)
		}
		if prefix == "" {
			t.Fatalf("empty prefix for %q", ns)
		}
	}
}

func TestW1iChainTurnRetryKeyPrefixEmpty(t *testing.T) {
	_, err := chainTurnRetryKeyPrefix("")
	if err == nil {
		t.Fatal("empty ns should fail")
	}
}

func TestW1iChainTurnRetryKeyPrefixColon(t *testing.T) {
	prefix, err := chainTurnRetryKeyPrefix("juhe-ai:test:")
	if err != nil {
		t.Fatalf("colon ns: %v", err)
	}
	if prefix == "" {
		t.Fatal("empty prefix")
	}
}

func TestW1iRedisGetJSONMissing(t *testing.T) {
	store, _ := w1iRedisStore(t, "test")
	raw, err := store.GetJSON(context.Background(), "missing")
	if err != nil || raw != nil {
		t.Fatalf("missing: raw=%v err=%v", raw, err)
	}
}

func TestW1iRedisGetJSONValid(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	client.Set(ctx, store.key("k1"), `{"a":1}`, 0)
	raw, err := store.GetJSON(ctx, "k1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(raw) != `{"a":1}` {
		t.Fatalf("raw=%s", raw)
	}
}

func TestW1iRedisGetJSONInvalidDeletesKey(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	client.Set(ctx, store.key("bad"), "not-json", 0)
	raw, err := store.GetJSON(ctx, "bad")
	if err != nil || raw != nil {
		t.Fatalf("invalid: raw=%v err=%v", raw, err)
	}
	if _, err := client.Get(ctx, store.key("bad")).Result(); err == nil {
		t.Fatal("invalid value should be deleted")
	}
}

func TestW1iRedisCompareSetMissingExpected(t *testing.T) {
	store, _ := w1iRedisStore(t, "test")
	ctx := context.Background()
	ok, err := store.CompareSetJSON(ctx, "new-key", nil, map[string]any{"state": "open"}, 60_000)
	if err != nil || !ok {
		t.Fatalf("cas missing: ok=%v err=%v", ok, err)
	}
}

func TestW1iRedisCompareSetConflict(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	client.Set(ctx, store.key("c1"), `{"state":"closed"}`, 0)
	ok, err := store.CompareSetJSON(ctx, "c1", nil, map[string]any{"state": "open"}, 60_000)
	if err != nil || ok {
		t.Fatalf("cas conflict: ok=%v err=%v", ok, err)
	}
}

func TestW1iRedisCompareSetMismatch(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	client.Set(ctx, store.key("c2"), `{"state":"closed"}`, 0)
	expected := json.RawMessage(`{"state":"other"}`)
	ok, err := store.CompareSetJSON(ctx, "c2", expected, map[string]any{"state": "open"}, 60_000)
	if err != nil || ok {
		t.Fatalf("cas mismatch: ok=%v err=%v", ok, err)
	}
}

func TestW1iRedisCompareSetMatch(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	expected := json.RawMessage(`{"state":"closed"}`)
	client.Set(ctx, store.key("c3"), string(expected), 0)
	ok, err := store.CompareSetJSON(ctx, "c3", expected, map[string]any{"state": "open"}, 60_000)
	if err != nil || !ok {
		t.Fatalf("cas match: ok=%v err=%v", ok, err)
	}
	got, _ := client.Get(ctx, store.key("c3")).Result()
	if got != `{"state":"open"}` {
		t.Fatalf("value=%s", got)
	}
}

func TestW1iRedisIncr(t *testing.T) {
	store, client := w1iRedisStore(t, "test")
	ctx := context.Background()
	v1, err := store.Incr(ctx, "retry-1", 30_000)
	if err != nil || v1 != 1 {
		t.Fatalf("incr1: %d %v", v1, err)
	}
	ttl := client.TTL(ctx, store.key("retry-1")).Val()
	if ttl <= 0 {
		t.Fatalf("ttl=%v", ttl)
	}
	v2, err := store.Incr(ctx, "retry-1", 30_000)
	if err != nil || v2 != 2 {
		t.Fatalf("incr2: %d %v", v2, err)
	}
}

func TestW1iNewStoreNilClient(t *testing.T) {
	store, err := newChainTurnRetryRedisStateStore(nil, "test")
	if err != nil || store != nil {
		t.Fatalf("nil client: %v %v", store, err)
	}
}

func TestW1iNewStoreValidNamespace(t *testing.T) {
	client := w1iSetupRedis(t)
	store, err := newChainTurnRetryRedisStateStore(client, "juhe-ai:prod")
	if err != nil || store == nil {
		t.Fatalf("valid store: %v %v", store, err)
	}
}
