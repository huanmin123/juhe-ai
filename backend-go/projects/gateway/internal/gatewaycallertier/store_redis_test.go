package gatewaycallertier

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// tierDriveSustainedDegradation 以固定时钟驱动 15 个连续分钟（每分钟 2 请求
// + 1 信号），构成 T2→T3 的最小完整路径（连续 15 劣化分钟 + 日样本 30/比率
// 50% 达标）。
func tierDriveSustainedDegradation(t *testing.T, recorder *Recorder, base time.Time, key string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < watchContinuationMinutes; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		recorder.now = func() time.Time { return at }
		recorder.RecordRequest(ctx, key)
		recorder.RecordRequest(ctx, key)
		recorder.RecordSignal(ctx, key, SignalInvalidJSON)
	}
}

// TestRedisMemoryEquivalenceAndLoss 双形态等价：同一驱动序列下层级结论一致；
// Redis FLUSH 后保守 T2；TTL 过期后保守 T2。
func TestRedisMemoryEquivalenceAndLoss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	base := tierTestNow(2026, 10, 17, 8, 0, 0)
	clock := base
	nowFn := func() time.Time { return clock }

	memory, err := NewRecorder(NewMemoryStore(nowFn), Metrics{}, nowFn)
	if err != nil {
		t.Fatal(err)
	}
	redisStore := NewRedisStore(client, "juhe-ai:caller-tiers:", nowFn)
	viaRedis, err := NewRecorder(redisStore, Metrics{}, nowFn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 前置：各记 20 个无信号请求（满足日样本分母且比率仍达标）。
	for _, recorder := range []*Recorder{memory, viaRedis} {
		recorder.now = func() time.Time { return base }
		for i := 0; i < minWindowRequests; i++ {
			recorder.RecordRequest(ctx, "key-eq")
		}
	}
	// 各驱动 15 连续劣化分钟。
	for _, recorder := range []*Recorder{memory, viaRedis} {
		tierDriveSustainedDegradation(t, recorder, base.Add(time.Minute), "key-eq")
	}
	if got := memory.TierOf(ctx, "key-eq"); got != TierWatch {
		t.Fatalf("memory 形态应降 T3: %d", got)
	}
	if got := viaRedis.TierOf(ctx, "key-eq"); got != TierWatch {
		t.Fatalf("redis 形态应降 T3（与 memory 等价）: %d", got)
	}

	// 状态丢失语义：FLUSH 后回 T2 保守默认。
	server.FlushDB()
	if got := viaRedis.TierOf(ctx, "key-eq"); got != DefaultTier {
		t.Fatalf("Redis 丢失必须保守 T2: %d", got)
	}

	// TTL 语义：滑动过期后回 T2。
	server.FlushDB()
	viaRedis.RecordRequest(ctx, "key-ttl")
	if !server.Exists("juhe-ai:caller-tiers:key-ttl") {
		t.Fatal("写入必须建立状态 key")
	}
	server.FastForward(StateTTL + time.Minute)
	if raw, err := redisStore.GetRaw(ctx, "key-ttl"); err != nil || raw != nil {
		t.Fatalf("TTL 过期必须视为无状态: %v %v", raw, err)
	}
	if got := viaRedis.TierOf(ctx, "key-ttl"); got != DefaultTier {
		t.Fatalf("TTL 过期必须保守 T2: %d", got)
	}
}
