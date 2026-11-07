package oauthmgmt

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// TestRedisSessionStoreCrossInstance 验证多节点 gateway 的核心前提：
// 授权在实例 A 写入的 session，实例 B 能读取并单次消费。
func TestRedisSessionStoreCrossInstance(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	storeA := newSessionStore(nil)
	storeB := newSessionStore(nil)
	for name, store := range map[string]*sessionStore{"A": storeA, "B": storeB} {
		backend, err := newRedisSessionBackend(client, "test-ns")
		if err != nil {
			t.Fatalf("newRedisSessionBackend(%s): %v", name, err)
		}
		store.setBackend(backend)
	}

	session := openAIOAuthSession{
		State:                "state-1",
		CodeVerifier:         "verifier",
		RedirectURI:          "http://localhost/callback",
		ClientID:             "client",
		OwnerSystemAccountID: "owner-1",
	}
	if err := storeA.setJSON(context.Background(), openAIOAuthSessionNamespace, "session-1", session, time.Minute); err != nil {
		t.Fatalf("set on A: %v", err)
	}
	raw, err := storeB.getJSON(context.Background(), openAIOAuthSessionNamespace, "session-1")
	if err != nil {
		t.Fatalf("get on B: %v", err)
	}
	if raw == nil {
		t.Fatal("B 必须读到 A 写入的 session")
	}
	var got openAIOAuthSession
	if err := unmarshalSession(raw, &got); err != nil || got.State != session.State {
		t.Fatalf("B 读到的 session 不符: %+v err=%v", got, err)
	}

	consumed, err := storeB.compareDeleteJSON(context.Background(), openAIOAuthSessionNamespace, "session-1", session)
	if err != nil || !consumed {
		t.Fatalf("B compareDelete 应成功: consumed=%v err=%v", consumed, err)
	}
	raw, err = storeA.getJSON(context.Background(), openAIOAuthSessionNamespace, "session-1")
	if err != nil {
		t.Fatalf("A get after consume: %v", err)
	}
	if raw != nil {
		t.Fatal("session 被 B 消费后 A 必须读不到")
	}
	consumed, err = storeA.compareDeleteJSON(context.Background(), openAIOAuthSessionNamespace, "session-1", session)
	if err != nil || consumed {
		t.Fatalf("重复消费必须失败: consumed=%v err=%v", consumed, err)
	}
}

func TestRedisSessionStoreTTLAndMismatch(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	backend, err := newRedisSessionBackend(client, "test-ns")
	if err != nil {
		t.Fatalf("newRedisSessionBackend: %v", err)
	}
	store := newSessionStore(nil)
	store.setBackend(backend)
	ctx := context.Background()
	session := map[string]any{"state": "expected"}
	if err := store.setJSON(ctx, "ns", "ttl", session, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	server.FastForward(2 * time.Minute)
	raw, err := store.getJSON(ctx, "ns", "ttl")
	if err != nil {
		t.Fatalf("get expired: %v", err)
	}
	if raw != nil {
		t.Fatal("过期 session 必须不可读")
	}

	if err := store.setJSON(ctx, "ns", "mismatch", session, time.Minute); err != nil {
		t.Fatalf("set mismatch: %v", err)
	}
	consumed, err := store.compareDeleteJSON(ctx, "ns", "mismatch", map[string]any{"state": "other"})
	if err != nil || consumed {
		t.Fatalf("错误 state 不得消费: consumed=%v err=%v", consumed, err)
	}
	raw, err = store.getJSON(ctx, "ns", "mismatch")
	if err != nil || raw == nil {
		t.Fatalf("错误 state 后 session 应保留: raw=%s err=%v", raw, err)
	}
}

func TestRedisSessionStoreBackendErrorIsNotMiss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	backend, err := newRedisSessionBackend(client, "test-ns")
	if err != nil {
		t.Fatalf("newRedisSessionBackend: %v", err)
	}
	store := newSessionStore(nil)
	store.setBackend(backend)
	_ = client.Close()
	raw, err := store.getJSON(context.Background(), "ns", "missing")
	if err == nil {
		t.Fatalf("Redis 故障必须返回错误而不是 cache miss: raw=%s", raw)
	}
	if err := store.setJSON(context.Background(), "ns", "key", map[string]any{"state": "s"}, time.Minute); err == nil {
		t.Fatal("Redis 故障时 setJSON 必须返回错误")
	}
	if _, err := store.compareDeleteJSON(context.Background(), "ns", "key", map[string]any{"state": "s"}); err == nil {
		t.Fatal("Redis 故障时 compareDeleteJSON 必须返回错误")
	}
}
