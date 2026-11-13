package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// BUG-0362 同构根治：realtime 每 API Key 连接计数改为 ZSET 槽集合（成员
// 独立租约）。以下用例覆盖跨实例占位、释放、租约过期与「一个连接的 ping
// 不能续活另一个已泄漏连接」的关键回归。
func TestRealtimeConnectionLimiterRedisCrossInstance(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiterA := newChainRealtimeConnectionLimiter(client, "realtime-test")
	limiterB := newChainRealtimeConnectionLimiter(client, "realtime-test")
	ctx := context.Background()

	tokenA, ok, err := limiterA.acquireContext(ctx, "key-1", 1)
	if err != nil || !ok || tokenA == "" {
		t.Fatalf("A first acquire: token=%q ok=%v err=%v", tokenA, ok, err)
	}
	if _, ok, err = limiterB.acquireContext(ctx, "key-1", 1); err != nil || ok {
		t.Fatalf("B must see A's connection and reject: ok=%v err=%v", ok, err)
	}
	limiterA.release("key-1", tokenA)
	tokenB, ok, err := limiterB.acquireContext(ctx, "key-1", 1)
	if err != nil || !ok || tokenB == "" {
		t.Fatalf("B acquire after A release: token=%q ok=%v err=%v", tokenB, ok, err)
	}
	limiterB.release("key-1", tokenB)
	// 重复释放幂等（ZREM 不存在成员不报错）。
	limiterA.release("key-1", tokenA)
}

func TestRealtimeConnectionLeaseExpiryAndRefreshIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiterA := newChainRealtimeConnectionLimiter(client, "realtime-test")
	limiterB := newChainRealtimeConnectionLimiter(client, "realtime-test")
	ctx := context.Background()
	nowMs := int64(1_800_000_000_000)
	clock := func() int64 { return nowMs }
	limiterA.remote.clock = clock
	limiterB.remote.clock = clock

	// token1 泄漏（不释放），token2 活跃并在 ping 中续租。
	token1, ok, err := limiterA.acquireContext(ctx, "key-lease", 2)
	if err != nil || !ok {
		t.Fatalf("token1 acquire: ok=%v err=%v", ok, err)
	}
	nowMs += 4 * time.Minute.Milliseconds()
	token2, ok, err := limiterA.acquireContext(ctx, "key-lease", 2)
	if err != nil || !ok {
		t.Fatalf("token2 acquire: ok=%v err=%v", ok, err)
	}
	nowMs += 4 * time.Minute.Milliseconds()
	limiterA.refresh("key-lease", token2)

	// 现在距 token1 占位 8 分钟（已过 10 分钟租约需再等），token2 已续到
	// now+10m。再推进 7 分钟：token1 绝对过期，token2 仍有效。
	nowMs += 7 * time.Minute.Milliseconds()
	if _, ok, err = limiterB.acquireContext(ctx, "key-lease", 2); err != nil || !ok {
		t.Fatalf("token2 的 ping 不得续活 token1；过期后应允许新连接: ok=%v err=%v", ok, err)
	}

	// 释放 token2 后，其成员删除；token1 的迟到释放仍幂等。
	limiterA.release("key-lease", token2)
	limiterA.release("key-lease", token1)
}

func TestRealtimeConnectionLimiterRedisErrorFailsClosed(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	limiter := newChainRealtimeConnectionLimiter(client, "realtime-test")
	_ = client.Close()
	if _, ok, err := limiter.acquireContext(context.Background(), "key-err", 1); err == nil || ok {
		t.Fatalf("Redis failure must return error and not acquire: ok=%v err=%v", ok, err)
	}
}

// C12 回归：refresh 必须区分「续租成功」与「槽已不存在」（Lua 0/1 →
// refreshed=false），调用方据此留痕；错误仍按 error 上抛。unknown token 与
// 已过期成员（score <= now 被脚本拒续）都不得复活为 true。
func TestRealtimeConnectionRefreshReportsMissingSlot(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := newChainRealtimeConnectionLimiter(client, "realtime-test")
	ctx := context.Background()
	nowMs := int64(1_800_000_000_000)
	limiter.remote.clock = func() int64 { return nowMs }

	token, ok, err := limiter.acquireContext(ctx, "key-refresh", 2)
	if err != nil || !ok || token == "" {
		t.Fatalf("acquire: token=%q ok=%v err=%v", token, ok, err)
	}
	if refreshed, err := limiter.remote.refresh(ctx, "key-refresh", token); err != nil || !refreshed {
		t.Fatalf("活槽续租必须 refreshed=true: refreshed=%v err=%v", refreshed, err)
	}
	if refreshed, err := limiter.remote.refresh(ctx, "key-refresh", "token-unknown"); err != nil || refreshed {
		t.Fatalf("未知 token 不得续租: refreshed=%v err=%v", refreshed, err)
	}
	// 已过期成员：把时钟推过租约期后，脚本按 score<=now 拒续（不复活）。
	nowMs += 11 * time.Minute.Milliseconds()
	if refreshed, err := limiter.remote.refresh(ctx, "key-refresh", token); err != nil || refreshed {
		t.Fatalf("过期槽不得被 refresh 复活: refreshed=%v err=%v", refreshed, err)
	}
}
