package gatewayquota

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// BUG-0362 同构根治回归：在途额度预留是 per-reservation 成员，同 scope 的
// 新预留不得续期旧预留；过期清理按成员精确扣减；迟到/重复 release 幂等。
func TestRedisInflightReservationIndependentExpiryAndIdempotentRelease(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	remote, err := newRedisInflightQuota(client, "inflight-slots-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	limits := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 100}}
	base := ScopeReserveInput{
		Scopes: []InflightScope{{ScopeKey: "api-key:key-1", Limits: limits}},
	}
	base.EstimatedCostUsd = 1
	_, first, err := remote.reserveScopes(ctx, base)
	if err != nil || len(first) != 1 || first[0].reservationToken == "" {
		t.Fatalf("first reserve: releases=%+v err=%v", first, err)
	}
	secondInput := base
	secondInput.EstimatedCostUsd = 2
	_, second, err := remote.reserveScopes(ctx, secondInput)
	if err != nil || len(second) != 1 || second[0].reservationToken == first[0].reservationToken {
		t.Fatalf("second reserve: releases=%+v err=%v", second, err)
	}

	// 把 first 的成员租约改到过去，再发起一次新预留：脚本只清理 first 自己的
	// 成员并扣 1，second 的 2 不受影响（旧实现会因同 scope 重置分数而永不回收）。
	member := scopeToken(first[0].stateKey) + "|" + first[0].reservationToken
	if err := client.ZAdd(ctx, remote.expiryKey, redis.Z{
		Score:  float64(time.Now().Add(-time.Minute).UnixMilli()),
		Member: member,
	}).Err(); err != nil {
		t.Fatalf("age first reservation: %v", err)
	}
	thirdInput := base
	thirdInput.EstimatedCostUsd = 0.5
	if _, _, err := remote.reserveScopes(ctx, thirdInput); err != nil {
		t.Fatalf("third reserve: %v", err)
	}
	states, err := remote.snapshot(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("snapshot: states=%+v err=%v", states, err)
	}
	if math.Abs(states[0].ReservedCostUsd-2.5) > 1e-9 {
		t.Fatalf("reserved cost = %v, want 2.5 (first expired, second+third kept)", states[0].ReservedCostUsd)
	}

	// 迟到 release first：成员已被清理，必须 no-op。
	if err := remote.release(ctx, first); err != nil {
		t.Fatalf("late release: %v", err)
	}
	if states, err = remote.snapshot(ctx); err != nil || math.Abs(states[0].ReservedCostUsd-2.5) > 1e-9 {
		t.Fatalf("late release must not subtract: states=%+v err=%v", states, err)
	}

	// second 释放两次：第一次精确扣 2，第二次幂等 no-op。
	if err := remote.release(ctx, second); err != nil {
		t.Fatalf("release second: %v", err)
	}
	if err := remote.release(ctx, second); err != nil {
		t.Fatalf("duplicate release: %v", err)
	}
	if states, err = remote.snapshot(ctx); err != nil || math.Abs(states[0].ReservedCostUsd-0.5) > 1e-9 {
		t.Fatalf("after release: states=%+v err=%v", states, err)
	}
}
