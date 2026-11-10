package gatewaysession

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func idsOf(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

func equalIDs(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderPersonalPromotesBoundAccount(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_order-personal"
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, nil),
		testAccount("b", 10, nil),
		testAccount("c", 10, nil),
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "b", scope); !ok {
		t.Fatal("claim failed")
	}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	// b moves to the front of its equal-tier run; peer order is preserved
	// (same-layer latch does not rotate peers).
	want := []string{"b", "a", "c"}
	if !equalIDs(idsOf(ordered), want) {
		t.Fatalf("ordered = %v, want %v", idsOf(ordered), want)
	}
}

func TestOrderPersonalStopsPromotionAtBetterPeer(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_order-stop"
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, nil),
		testAccount("b", 5, nil),
		testAccount("c", 5, nil),
	}
	// b is bound; a is strictly better (higher priority), so b cannot pass a
	// but can rotate with c.
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "b", scope); !ok {
		t.Fatal("claim failed")
	}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !equalIDs(idsOf(ordered), want) {
		t.Fatalf("ordered = %v, want %v", idsOf(ordered), want)
	}
}

func TestOrderPersonalBoundNormalStaysBelowSuperPriority(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_order-super"
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, nil),
		testAccount("b", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.SuperPriorityEnabled = true }),
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "a", scope); !ok {
		t.Fatal("claim failed")
	}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	// super priority is a higher dispatch layer; the bound normal account
	// cannot promote over it (and the removal of the old anySuperPriority
	// short-circuit no longer disables affinity for the whole group).
	if !equalIDs(idsOf(ordered), []string{"a", "b"}) {
		t.Fatalf("ordered = %v, want normal bound below super", idsOf(ordered))
	}
}

func TestOrderPersonalWithoutBindingUnchanged(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{testAccount("a", 1, nil), testAccount("b", 2, nil)}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, "aff_v1_none", DispatchOrderingOptions{GroupType: GroupTypePersonal})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"a", "b"}) {
		t.Fatalf("ordered = %v, want unchanged", idsOf(ordered))
	}
}

func TestOrderTrafficMigrationPreferencePutsTargetFirst(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	service.RememberOpenAIAccountTrafficMigrationPreference("source", "target", scope)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("target", 5, nil),
		testAccount("x", 5, nil),
		testAccount("y", 5, nil),
	}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypePersonal,
		TrafficMigrationScope: scope,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	want := []string{"target", "x", "y"}
	if !equalIDs(idsOf(ordered), want) {
		t.Fatalf("ordered = %v, want %v", idsOf(ordered), want)
	}
}

func TestOrderSoftConcurrencyPrefersBoundAndLessLoaded(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_soft_1"
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("busy", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(4) }),
		testAccount("free", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
		testAccount("bound", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "bound", scope); !ok {
		t.Fatal("claim failed")
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 5, ClientIPConcurrencyLimit: 0}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{
		GroupType:        GroupTypePersonal,
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	// bound wins via affinityAllowed; free/busy tie on softBusy=false and load
	// ratio decides (1/5 vs 4/5).
	want := []string{"bound", "free", "busy"}
	if !equalIDs(idsOf(ordered), want) {
		t.Fatalf("ordered = %v, want %v", idsOf(ordered), want)
	}
}

func TestOrderSoftConcurrencyHardBusyLast(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("busy", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(10) }),
		testAccount("free", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 5}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, "", DispatchOrderingOptions{
		GroupType:        GroupTypePersonal,
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"free", "busy"}) {
		t.Fatalf("ordered = %v, want [free busy]", idsOf(ordered))
	}
}

func TestOrderSoftConcurrencyOwnerYieldsAtThreshold(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_soft_owner"
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("owner", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(5) }),
		testAccount("peer", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "owner", scope); !ok {
		t.Fatal("claim failed")
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 5}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, key, DispatchOrderingOptions{
		GroupType:        GroupTypePersonal,
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	// The owner is soft-busy (5/5), so the healthy peer must lead; the owner
	// keeps affinity only when it drops below the threshold.
	if !equalIDs(idsOf(ordered), []string{"peer", "owner"}) {
		t.Fatalf("ordered = %v, want [peer owner]", idsOf(ordered))
	}
}

func TestOrderSoftConcurrencyTrafficMigrationTargetFirst(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
		testAccount("b", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
		testAccount("target", 10, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(1) }),
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 5}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, "", DispatchOrderingOptions{
		GroupType:        GroupTypePersonal,
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"a", "b", "target"}) {
		t.Fatalf("ordered = %v, want stable input order", idsOf(ordered))
	}
	// The explicit traffic-migration preference still overrides load order.
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	service.RememberOpenAIAccountTrafficMigrationPreference("source", "target", scope)
	ordered, err = service.OrderOpenAIAccountsBySessionAffinity(accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypePersonal,
		SchedulingPolicy:      policy,
		TrafficMigrationScope: scope,
	})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"target", "a", "b"}) {
		t.Fatalf("ordered = %v, want target first", idsOf(ordered))
	}
}

func TestCompareGatewayAccountModelPriorityProjection(t *testing.T) {
	priority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{
		"direct": ModelPriorityRankDirect,
		"mapped": ModelPriorityRankMapping,
		"none":   ModelPriorityRankUnsupported,
	}}
	if delta := CompareGatewayAccountModelPriority("direct", "mapped", priority); delta >= 0 {
		t.Fatalf("direct vs mapped delta = %d", delta)
	}
	if delta := CompareGatewayAccountModelPriority("missing", "direct", priority); delta <= 0 {
		t.Fatalf("missing (unsupported) vs direct delta = %d", delta)
	}
	if delta := CompareGatewayAccountModelPriority("x", "y", nil); delta != 0 {
		t.Fatalf("nil priority delta = %d", delta)
	}
}
