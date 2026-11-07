package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// TestChainConcurrencyStoreRedisCrossInstance 验证生产接线后的多副本语义：
// 两个 gateway 适配器共享同一 Redis 计数，第二个实例能看到第一个实例的槽。
func TestChainConcurrencyStoreRedisCrossInstance(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	newStore := func() *chainConcurrencyStore {
		tracker, err := gatewayclientip.NewRedisAccountConcurrency(gatewayclientip.RedisAccountConcurrencyOptions{
			Client:    client,
			Namespace: "chain-concurrency-test",
		})
		if err != nil {
			t.Fatalf("NewRedisAccountConcurrency: %v", err)
		}
		return newChainConcurrencyStore(tracker)
	}
	storeA := newStore()
	storeB := newStore()
	ctx := context.Background()

	slotA, err := storeA.TryAcquireAsync(ctx, "acc-cross", 1, gatewaydispatchAcquireOptions())
	if err != nil || !slotA.Acquired || slotA.Current != 1 {
		t.Fatalf("A 首次占位失败: slot=%+v err=%v", slotA, err)
	}
	slotB, err := storeB.TryAcquireAsync(ctx, "acc-cross", 1, gatewaydispatchAcquireOptions())
	if err != nil {
		t.Fatalf("B 占位返回错误: %v", err)
	}
	if slotB.Acquired || slotB.Current != 1 {
		t.Fatalf("B 必须看到 A 的占槽并拒绝: %+v", slotB)
	}
	slotA.Release()

	slotB, err = storeB.TryAcquireAsync(ctx, "acc-cross", 1, gatewaydispatchAcquireOptions())
	if err != nil || !slotB.Acquired || slotB.Current != 1 {
		t.Fatalf("A 释放后 B 应占位成功: slot=%+v err=%v", slotB, err)
	}
	slotB.Release()
}

func gatewaydispatchAcquireOptions() gatewaydispatch.AccountConcurrencyAcquireOptions {
	return gatewaydispatch.AccountConcurrencyAcquireOptions{
		Lane:     gatewayclientip.AccountConcurrencyLaneText,
		RPMLimit: 2,
	}
}
