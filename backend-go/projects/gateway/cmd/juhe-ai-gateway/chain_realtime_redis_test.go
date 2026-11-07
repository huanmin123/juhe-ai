package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestRealtimeConnectionLimiterRedisCrossInstance(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiterA := newChainRealtimeConnectionLimiter(client, "realtime-test")
	limiterB := newChainRealtimeConnectionLimiter(client, "realtime-test")
	ctx := context.Background()

	ok, err := limiterA.acquireContext(ctx, "key-1", 1)
	if err != nil || !ok {
		t.Fatalf("A first acquire: ok=%v err=%v", ok, err)
	}
	ok, err = limiterB.acquireContext(ctx, "key-1", 1)
	if err != nil || ok {
		t.Fatalf("B must see A's connection and reject: ok=%v err=%v", ok, err)
	}
	limiterA.release("key-1")
	ok, err = limiterB.acquireContext(ctx, "key-1", 1)
	if err != nil || !ok {
		t.Fatalf("B acquire after A release: ok=%v err=%v", ok, err)
	}
	limiterB.release("key-1")

	// TTL 兜底：实例崩溃未 release 时，计数在 TTL 后自愈。
	ok, err = limiterA.acquireContext(ctx, "key-ttl", 1)
	if err != nil || !ok {
		t.Fatalf("TTL acquire: ok=%v err=%v", ok, err)
	}
	// 活跃会话通过 ping 续租：临近过期前续租后仍占位，超过 TTL 才自愈。
	server.FastForward(realtimeConnectionRedisTTL - time.Minute)
	limiterA.refresh("key-ttl")
	server.FastForward(realtimeConnectionRedisTTL - time.Minute)
	ok, err = limiterB.acquireContext(ctx, "key-ttl", 1)
	if err != nil || ok {
		t.Fatalf("refreshed lease must still block: ok=%v err=%v", ok, err)
	}
	server.FastForward(2 * time.Minute)
	ok, err = limiterB.acquireContext(ctx, "key-ttl", 1)
	if err != nil || !ok {
		t.Fatalf("after TTL another instance must acquire: ok=%v err=%v", ok, err)
	}
}

func TestRealtimeConnectionLimiterRedisErrorFailsClosed(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	limiter := newChainRealtimeConnectionLimiter(client, "realtime-test")
	_ = client.Close()
	if ok, err := limiter.acquireContext(context.Background(), "key-err", 1); err == nil || ok {
		t.Fatalf("Redis failure must return error and not acquire: ok=%v err=%v", ok, err)
	}
}
