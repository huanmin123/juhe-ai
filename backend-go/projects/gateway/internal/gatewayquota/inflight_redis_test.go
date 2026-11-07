package gatewayquota

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func newRedisInflightTestService(t *testing.T) (*InflightQuotaService, *redisInflightQuota, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service, err := NewInflightQuotaService(InflightQuotaConfig{
		Redis:          client,
		RedisNamespace: "inflight-test",
		Timer:          func(time.Duration, func()) func() { return func() {} },
	})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	return service, service.remote, server
}

func TestRedisInflightQuotaAtomicMultiScope(t *testing.T) {
	service, remote, _ := newRedisInflightTestService(t)
	ctx := context.Background()
	limits10 := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 10}}
	limits100 := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 100}}
	input := ScopeReserveInput{
		Scopes: []InflightScope{
			{ScopeKey: "api-key:key-1", Limits: limits10, CurrentCosts: RequestQuotaCosts{Total: 9}},
			{ScopeKey: "auth:scope-1", Limits: limits100, CurrentCosts: RequestQuotaCosts{}},
		},
		EstimatedCostUsd: 2,
	}
	decision, releases, err := remote.reserveScopes(ctx, input)
	if err != nil {
		t.Fatalf("reserve denied arm: %v", err)
	}
	if decision.Allowed || decision.DeniedScopeKey != "api-key:key-1" || len(releases) != 0 {
		t.Fatalf("API Key scope must deny the whole reservation: %+v releases=%d", decision, len(releases))
	}
	if states, err := remote.snapshot(ctx); err != nil || len(states) != 0 {
		t.Fatalf("denied reservation must not write any scope: states=%+v err=%v", states, err)
	}

	// 降低已结算成本后整体放行：两个 scope 同时记账。
	input.Scopes[0].CurrentCosts = RequestQuotaCosts{}
	decision, releases, err = remote.reserveScopes(ctx, input)
	if err != nil || !decision.Allowed || len(releases) != 2 {
		t.Fatalf("allowed reservation: decision=%+v releases=%d err=%v", decision, len(releases), err)
	}
	states, err := remote.snapshot(ctx)
	if err != nil || len(states) != 2 {
		t.Fatalf("snapshot after reserve: states=%+v err=%v", states, err)
	}
	for _, state := range states {
		if state.ReservedCostUsd != 2 {
			t.Fatalf("reserved cost = %v, want 2 (%+v)", state.ReservedCostUsd, state)
		}
	}

	// 第二实例看到同一预留：已结算 9 + 在途 2 > 10，拒绝。
	otherService, err := NewInflightQuotaService(InflightQuotaConfig{
		Redis:          remote.client,
		RedisNamespace: "inflight-test",
		Timer:          func(time.Duration, func()) func() { return func() {} },
	})
	if err != nil {
		t.Fatalf("second service: %v", err)
	}
	otherRemote := otherService.remote
	deniedInput := ScopeReserveInput{
		Scopes:           []InflightScope{{ScopeKey: "api-key:key-1", Limits: limits10, CurrentCosts: RequestQuotaCosts{Total: 9}}},
		EstimatedCostUsd: 0.5,
	}
	deniedDecision, _, err := otherRemote.reserveScopes(ctx, deniedInput)
	if err != nil || deniedDecision.Allowed {
		t.Fatalf("second instance must see reserved cost and deny: decision=%+v err=%v", deniedDecision, err)
	}

	if err := service.remote.release(ctx, releases); err != nil {
		t.Fatalf("release: %v", err)
	}
	if states, err := remote.snapshot(ctx); err != nil || len(states) != 0 {
		t.Fatalf("snapshot after release: states=%+v err=%v", states, err)
	}
}

func TestRedisInflightQuotaExpiryAndFailure(t *testing.T) {
	service, remote, _ := newRedisInflightTestService(t)
	ctx := context.Background()
	input := ReserveInput{
		APIKeyID:         "key-ttl",
		Limits:           RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 10}},
		CurrentCosts:     RequestQuotaCosts{Total: 9},
		EstimatedCostUsd: 2,
	}
	decision, err := service.ReserveContext(ctx, input)
	if err != nil || decision.Allowed {
		t.Fatalf("initial reservation should deny: decision=%+v err=%v", decision, err)
	}
	input.CurrentCosts = RequestQuotaCosts{}
	decision, err = service.ReserveContext(ctx, input)
	if err != nil || !decision.Allowed || decision.Reservation == nil {
		t.Fatalf("initial reservation should allow: decision=%+v err=%v", decision, err)
	}
	// miniredis 的 FastForward 不改变 ZSET 分数（分数来自 Go 时钟），直接把
	// 该 scope 的过期分数改到过去，验证 snapshot 的惰性清理。
	if err := remote.client.ZAdd(ctx, remote.expiryKey, redis.Z{
		Score:  float64(time.Now().Add(-time.Minute).UnixMilli()),
		Member: scopeToken("key-ttl"),
	}).Err(); err != nil {
		t.Fatalf("age expiry: %v", err)
	}
	if states, err := remote.snapshot(ctx); err != nil || len(states) != 0 {
		t.Fatalf("expired reservation must be cleaned: states=%+v err=%v", states, err)
	}
	decision, err = service.ReserveContext(ctx, input)
	if err != nil || !decision.Allowed {
		t.Fatalf("reservation after TTL should allow: decision=%+v err=%v", decision, err)
	}

	_ = remote.client.Close()
	if _, err := service.ReserveContext(ctx, input); err == nil {
		t.Fatal("Redis failure must returned as an error (fail-closed)")
	}
}
