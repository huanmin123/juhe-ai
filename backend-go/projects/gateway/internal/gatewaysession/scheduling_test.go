package gatewaysession

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestResolveGroupSchedulingPolicyDefaults(t *testing.T) {
	defaults := SchedulingDefaults{GlobalMax: 5000}

	t.Run("non high concurrency is nil without validation", func(t *testing.T) {
		policy, err := ResolveGroupSchedulingPolicy(GroupTypePersonal, map[string]any{"bogus": 1}, defaults)
		if err != nil || policy != nil {
			t.Fatalf("policy = %v, err = %v", policy, err)
		}
	})

	t.Run("empty policy yields defaults", func(t *testing.T) {
		policy, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, nil, defaults)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := DefaultHighConcurrencyGroupSchedulingPolicy(defaults)
		if *policy != want {
			t.Fatalf("policy = %+v, want %+v", *policy, want)
		}
	})

	t.Run("valid overrides", func(t *testing.T) {
		policy, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{
			"fastFirstEnabled":         false,
			"slowRequestThresholdMs":   1000.0,
			"imageLaneMaxConcurrency":  2,
			"breakAffinityOnSoftLimit": false,
		}, defaults)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy.FastFirstEnabled {
			t.Fatal("fastFirstEnabled should be false")
		}
		if policy.SlowRequestThresholdMs != 1000 {
			t.Fatalf("slowRequestThresholdMs = %d", policy.SlowRequestThresholdMs)
		}
		if policy.ImageLaneMaxConcurrency != 2 {
			t.Fatalf("imageLaneMaxConcurrency = %d", policy.ImageLaneMaxConcurrency)
		}
		if policy.BreakAffinityOnSoftLimit {
			t.Fatal("breakAffinityOnSoftLimit should be false")
		}
	})

	t.Run("errors mirror Node messages", func(t *testing.T) {
		tests := []struct {
			name    string
			policy  map[string]any
			message string
		}{
			{
				name:    "unknown key",
				policy:  map[string]any{"wat": 1},
				message: "分组调度策略包含未知字段：wat",
			},
			{
				name:    "boolean type",
				policy:  map[string]any{"fastFirstEnabled": "yes"},
				message: "分组调度策略 fastFirstEnabled 必须是布尔值",
			},
			{
				name:    "integer type",
				policy:  map[string]any{"slowRequestThresholdMs": 1.5},
				message: "分组调度策略 slowRequestThresholdMs 必须是整数",
			},
			{
				name:    "bounds",
				policy:  map[string]any{"slowRequestThresholdMs": 2000000.0},
				message: "分组调度策略 slowRequestThresholdMs 必须在 1-1000000 之间",
			},
			{
				name:    "mode",
				policy:  map[string]any{"mode": "other"},
				message: "分组调度策略 mode 无效",
			},
			{
				name:    "overflow mode",
				policy:  map[string]any{"clientIpConcurrencyOverflowMode": "other"},
				message: "分组调度策略 clientIpConcurrencyOverflowMode 无效",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, tt.policy, defaults)
				if err == nil || err.Error() != tt.message {
					t.Fatalf("err = %v, want %q", err, tt.message)
				}
			})
		}
	})
}

func TestEffectiveConcurrencyLimits(t *testing.T) {
	defaults := SchedulingDefaults{GlobalMax: 5000}
	t.Run("soft limit clamps to hard limit", func(t *testing.T) {
		got, err := EffectiveSoftConcurrencyLimit(3, nil, defaults)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 3 {
			t.Fatalf("soft limit = %d, want 3", got)
		}
	})
	t.Run("image lane defaults to hard limit", func(t *testing.T) {
		got, err := EffectiveImageLaneConcurrencyLimit(4, nil, defaults)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 4 {
			t.Fatalf("image lane limit = %d, want 4", got)
		}
	})
	t.Run("image lane configured below hard limit", func(t *testing.T) {
		got, err := EffectiveImageLaneConcurrencyLimit(10, map[string]any{"imageLaneMaxConcurrency": 2.0}, defaults)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 2 {
			t.Fatalf("image lane limit = %d, want 2", got)
		}
	})
}

func TestOrderAsyncMixedDrivers(t *testing.T) {
	// cacheDriver=redis but runtimeStateDriver=memory: the high-concurrency
	// ordering must fall back to the process-local in-flight stats while the
	// binding still comes from Redis.
	concurrency := newMockConcurrency()
	redisService, _, _, _ := newRedisAffinityService(t)
	redisService.cfg.RuntimeStateDriver = RuntimeStateDriverMemory
	redisService.cfg.Concurrency = concurrency
	concurrency.SetInFlight("a", AccountInFlightStats{CurrentConcurrency: 4})
	concurrency.SetInFlight("b", AccountInFlightStats{CurrentConcurrency: 1})

	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_mixed"
	if _, ok := redisService.ClaimOpenAIAccountForSessionAsync(ctx, key, "b", scope); !ok {
		t.Fatal("claim failed")
	}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, nil),
		testAccount("b", 10, nil),
	}
	ordered, err := redisService.OrderOpenAIAccountsBySessionAffinityAsync(ctx, accounts, key, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if ids := idsOf(ordered); !equalIDs(ids, []string{"b", "a"}) {
		t.Fatalf("ordered = %v, want [b a]", ids)
	}
	_ = context.Background
}
