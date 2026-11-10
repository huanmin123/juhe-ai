package gatewaysession

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

func TestResolveGroupSchedulingPolicyDefaults(t *testing.T) {
	t.Run("nil policy means both disabled", func(t *testing.T) {
		policy, err := ResolveGroupSchedulingPolicy(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy.DefaultSoftConcurrency != -1 || policy.ClientIPConcurrencyLimit != 0 {
			t.Fatalf("policy = %+v", *policy)
		}
	})

	t.Run("two-key policy", func(t *testing.T) {
		policy, err := ResolveGroupSchedulingPolicy(&gatewayscheduling.Policy{
			DefaultSoftConcurrency:   5,
			ClientIPConcurrencyLimit: 3,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy.DefaultSoftConcurrency != 5 || policy.ClientIPConcurrencyLimit != 3 {
			t.Fatalf("policy = %+v", *policy)
		}
	})
}

func TestEffectiveSoftConcurrencyLimit(t *testing.T) {
	t.Run("disabled returns zero", func(t *testing.T) {
		got, err := EffectiveSoftConcurrencyLimit(10, nil)
		if err != nil || got != 0 {
			t.Fatalf("soft limit = %d, err = %v", got, err)
		}
	})
	t.Run("threshold clamps to hard limit", func(t *testing.T) {
		policy := &gatewayscheduling.Policy{DefaultSoftConcurrency: 20}
		got, err := EffectiveSoftConcurrencyLimit(10, policy)
		if err != nil || got != 10 {
			t.Fatalf("soft limit = %d, err = %v", got, err)
		}
	})
	t.Run("threshold below hard limit", func(t *testing.T) {
		policy := &gatewayscheduling.Policy{DefaultSoftConcurrency: 3}
		got, err := EffectiveSoftConcurrencyLimit(10, policy)
		if err != nil || got != 3 {
			t.Fatalf("soft limit = %d, err = %v", got, err)
		}
	})
}

func TestOrderAsyncSoftConcurrencyUsesSnapshot(t *testing.T) {
	redisService, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_mixed"
	if _, ok := redisService.ClaimOpenAIAccountForSessionAsync(ctx, key, "owner", scope); !ok {
		t.Fatal("claim failed")
	}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("owner", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(5) }),
		testAccount("peer", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 5}
	ordered, err := redisService.OrderOpenAIAccountsBySessionAffinityAsync(ctx, accounts, key, DispatchOrderingOptions{
		GroupType:        GroupTypePersonal,
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if ids := idsOf(ordered); !equalIDs(ids, []string{"peer", "owner"}) {
		t.Fatalf("ordered = %v, want [peer owner]", ids)
	}
}
