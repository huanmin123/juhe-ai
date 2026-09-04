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
	// b promotes over both equal-tier peers and rotates within the tier.
	want := []string{"b", "c", "a"}
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

func TestOrderPersonalNoopWhenSuperPriorityPresent(t *testing.T) {
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
	if !equalIDs(idsOf(ordered), []string{"a", "b"}) {
		t.Fatalf("ordered = %v, want untouched", idsOf(ordered))
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

func TestOrderHighConcurrencyPrefersBoundAndLessLoaded(t *testing.T) {
	concurrency := newMockConcurrency()
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = concurrency
	})
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	key := "aff_v1_hc"
	concurrency.SetInFlight("busy", AccountInFlightStats{CurrentConcurrency: 4})
	concurrency.SetInFlight("free", AccountInFlightStats{CurrentConcurrency: 1})
	concurrency.SetInFlight("bound", AccountInFlightStats{CurrentConcurrency: 1})
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("busy", 10, nil),
		testAccount("free", 10, nil),
		testAccount("bound", 10, nil),
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), key, "bound", scope); !ok {
		t.Fatal("claim failed")
	}
	ordered, err := service.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, key, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency})
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	// bound wins via affinityAllowed; the remaining two tie on load ratio
	// (1/5) and current concurrency, so the stable input order decides.
	want := []string{"bound", "free", "busy"}
	if !equalIDs(idsOf(ordered), want) {
		t.Fatalf("ordered = %v, want %v", idsOf(ordered), want)
	}
}

func TestOrderHighConcurrencyHardBusyLastWhenFastFirstDisabled(t *testing.T) {
	concurrency := newMockConcurrency()
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = concurrency
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
	})
	concurrency.SetCurrent("free", 1)
	concurrency.SetCurrent("busy", 5)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("busy", 10, nil),
		testAccount("free", 10, nil),
	}
	ordered, err := service.orderOpenAIHighConcurrencyHardBusyLastAsync(context.Background(), accounts)
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"free", "busy"}) {
		t.Fatalf("ordered = %v, want [free busy]", idsOf(ordered))
	}
}

func TestOrderHighConcurrencyTrafficMigrationTargetFirstWithPolicy(t *testing.T) {
	concurrency := newMockConcurrency()
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = concurrency
	})
	for _, id := range []string{"a", "b", "target"} {
		concurrency.SetInFlight(id, AccountInFlightStats{CurrentConcurrency: 1})
	}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 10, nil),
		testAccount("b", 10, nil),
		testAccount("target", 10, nil),
	}
	ordered, err := service.orderOpenAIHighConcurrencyAccounts(accounts, "", nil, "target", nil)
	if err != nil {
		t.Fatalf("order error: %v", err)
	}
	if !equalIDs(idsOf(ordered), []string{"target", "a", "b"}) {
		t.Fatalf("ordered = %v, want target first", idsOf(ordered))
	}
}

func TestOrderHighConcurrencyInvalidPolicyErrors(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{testAccount("a", 1, nil), testAccount("b", 1, nil)}
	_, err := service.orderOpenAIHighConcurrencyAccounts(accounts, "", map[string]any{"unknownField": 1.0}, "", nil)
	if err == nil {
		t.Fatal("expected unknown policy key error")
	}
}

func TestAreHighConcurrencyAccountsHardBusy(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 1, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(5) }),
		testAccount("b", 1, func(a *gatewayruntimecache.OpenAIAccountSecret) { a.CurrentConcurrency = ptrInt(9) }),
	}
	if !service.AreOpenAIHighConcurrencyAccountsHardBusy(accounts, DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}) {
		t.Fatal("expected hard busy")
	}
	if service.AreOpenAIHighConcurrencyAccountsHardBusy(accounts[:1], DispatchOrderingOptions{GroupType: GroupTypePersonal}) {
		t.Fatal("personal groups never report hard busy")
	}
}

func TestAreHighConcurrencyAccountsBusyForLane(t *testing.T) {
	concurrency := newMockConcurrency()
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = concurrency
	})
	accounts := []gatewayruntimecache.OpenAIAccountSecret{testAccount("img", 1, nil)}
	// Total lane free, image lane at limit with imageLaneMaxConcurrency=1.
	concurrency.SetLane(RequestLaneImage, "img", 1)
	busy, err := service.AreOpenAIHighConcurrencyAccountsBusyForLane(accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{
			GroupType:        GroupTypeHighConcurrency,
			SchedulingPolicy: map[string]any{"imageLaneMaxConcurrency": 1},
		},
		RequestLane: RequestLaneImage,
	})
	if err != nil {
		t.Fatalf("busy error: %v", err)
	}
	if !busy {
		t.Fatal("expected image lane busy")
	}
	// Without the image lane the same accounts are free.
	busy, err = service.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency},
	})
	if err != nil {
		t.Fatalf("busy async error: %v", err)
	}
	if busy {
		t.Fatal("expected text lane free")
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
