package gatewayproxyhealth

import (
	"strings"
	"testing"
)

func speedFirstConfig() SpeedFirstRuntimeConfig {
	return SpeedFirstRuntimeConfig{
		FirstByteDeadlineMs:           3_000,
		SlowTriggerCount:              3,
		SlowWindowSeconds:             60,
		RecoverySuccessCount:          3,
		ProbeIntervalSeconds:          30,
		DegradedTTLSeconds:            120,
		MaxFirstByteRetriesPerRequest: 2,
	}
}

func latencyScope() *LatencyDegradationScope {
	return &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: "g1"}
}

func latencyAccount(id string) SuppressibleGatewayAccount {
	return SuppressibleGatewayAccount{ID: id, Name: "Account " + id}
}

func TestRecordNormalRouteFirstByteSlowTriggerBoundary(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")

	// Two slow samples: below the trigger of 3, not degraded.
	for i := 0; i < 2; i++ {
		result, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, "")
		if err != nil || result == nil {
			t.Fatalf("slow %d: result=%v err=%v", i, result, err)
		}
		if result.Degraded {
			t.Fatalf("slow %d must not degrade: %+v", i, result)
		}
		if result.SlowCount != int64(i+1) {
			t.Fatalf("slow %d count = %d", i, result.SlowCount)
		}
	}

	// The third sample crosses the trigger exactly.
	result, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, "")
	if err != nil || result == nil || !result.Degraded {
		t.Fatalf("trigger sample: %+v err=%v", result, err)
	}
	if result.DegradedUntil == nil || *result.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("degradedUntil = %v", result.DegradedUntil)
	}
	if result.NextProbeAt == nil {
		t.Fatal("trigger sample must schedule the recovery probe")
	}

	// Repeated slow samples while degraded must not extend the bounded TTL.
	clock.Advance(30_000)
	resultAgain, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, "")
	if err != nil || resultAgain == nil || !resultAgain.Degraded {
		t.Fatalf("degraded slow: %+v err=%v", resultAgain, err)
	}
	if resultAgain.DegradedUntil == nil || *resultAgain.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("degradedUntil must stay bounded, got %v", resultAgain.DegradedUntil)
	}
	// slowCount keeps growing so triggeredDegraded stays true: the probe is
	// rescheduled into the future while the degradedUntil deadline stays put.
	if resultAgain.NextProbeAt == nil || result.NextProbeAt == nil || *resultAgain.NextProbeAt == *result.NextProbeAt {
		t.Fatalf("repeat slow must reschedule the probe: %v vs %v", resultAgain.NextProbeAt, result.NextProbeAt)
	}

	// Outside the slow window the counter restarts at 1.
	clock.Set(1_000_000 + 120_000 + 61_000)
	afterWindow, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, "")
	if err != nil || afterWindow == nil {
		t.Fatalf("window expiry: %+v err=%v", afterWindow, err)
	}
	if afterWindow.SlowCount != 1 {
		t.Fatalf("slowCount after window = %d", afterWindow.SlowCount)
	}
	if afterWindow.Degraded {
		t.Fatal("window expiry must reset the degradation")
	}
}

func TestRecordNormalRouteFirstByteSuccessRecoveryBoundary(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	// First byte above the deadline is ignored.
	fast := int64(1_000)
	slow := int64(9_000)
	result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, scope, &config, &slow)
	if err != nil || result != nil {
		t.Fatalf("slow success must be ignored: %+v err=%v", result, err)
	}

	// Two successes below the required three keep the degraded state.
	for i := int64(1); i <= 2; i++ {
		clock.Advance(1_000)
		result, err = service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, scope, &config, &fast)
		if err != nil || result == nil {
			t.Fatalf("success %d: %+v err=%v", i, result, err)
		}
		if result.Cleared {
			t.Fatalf("success %d must not clear yet", i)
		}
		if result.RecoverySuccessCount != i || result.RequiredRecoverySuccessCount != 3 {
			t.Fatalf("success %d result: %+v", i, result)
		}
	}
	// The third success clears exactly at the required count.
	clock.Advance(1_000)
	result, err = service.RecordNormalRouteFirstByteSuccess(contextBackground(), account, scope, &config, &fast)
	if err != nil || result == nil || !result.Cleared {
		t.Fatalf("third success must clear: %+v err=%v", result, err)
	}
	// Degradation lapsed in between must short-circuit the clearing too.
	if degraded, err := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), account, scope); err != nil || degraded {
		t.Fatalf("degraded after clear = %v err=%v", degraded, err)
	}
}

func TestOrderGatewayAccountsByLatencyDegradation(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	a := latencyAccount("a")
	b := latencyAccount("b")

	result, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(contextBackground(), service,
		[]SuppressibleGatewayAccount{a, b},
		func(account SuppressibleGatewayAccount) SuppressibleGatewayAccount { return account },
		scope, &config, nil)
	if err != nil || result.Applied {
		t.Fatalf("fresh order: %+v err=%v", result, err)
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), a, scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), a, scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), a, scope, &config, ""); err != nil {
		t.Fatal(err)
	}

	// Degraded single account: nobody left to reorder to.
	result, err = OrderGatewayAccountsByNormalRouteLatencyDegradation(contextBackground(), service,
		[]SuppressibleGatewayAccount{a},
		func(account SuppressibleGatewayAccount) SuppressibleGatewayAccount { return account },
		scope, &config, nil)
	if err != nil || result.Applied || !result.BypassedAllDegraded {
		t.Fatalf("bypassed order: %+v err=%v", result, err)
	}
	if len(result.DegradedAccountIDs) != 1 || result.DegradedAccountIDs[0] != "a" {
		t.Fatalf("degraded ids = %v", result.DegradedAccountIDs)
	}

	// Mixed: healthy accounts first, degraded last.
	result, err = OrderGatewayAccountsByNormalRouteLatencyDegradation(contextBackground(), service,
		[]SuppressibleGatewayAccount{a, b},
		func(account SuppressibleGatewayAccount) SuppressibleGatewayAccount { return account },
		scope, &config, nil)
	if err != nil || !result.Applied || result.BypassedAllDegraded {
		t.Fatalf("mixed order: %+v err=%v", result, err)
	}
	if result.Accounts[0].ID != "b" || result.Accounts[1].ID != "a" {
		t.Fatalf("mixed account order = %v", idsSuppressed(result.Accounts))
	}
}

func idsSuppressed(accounts []SuppressibleGatewayAccount) []string {
	output := make([]string, 0, len(accounts))
	for _, account := range accounts {
		output = append(output, account.ID)
	}
	return output
}

func TestLatencyProbeRoundRecovery(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Candidates are only listed once the recovery probe is due (5s + jitter).
	future := int64(1_000_000 + 1)
	early, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, &future)
	if err != nil || len(early) != 0 {
		t.Fatalf("early candidates = %v err=%v", early, err)
	}
	clock.Advance(5_002)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %v err=%v", candidates, err)
	}

	claim, err := service.AcquireNormalRouteLatencyProbeClaim(contextBackground(), candidates[0])
	if err != nil || claim == nil {
		t.Fatalf("claim = %v err=%v", claim, err)
	}
	// A second claim on the same candidate must be refused.
	second, err := service.AcquireNormalRouteLatencyProbeClaim(contextBackground(), candidates[0])
	if err != nil || second != nil {
		t.Fatalf("second claim = %v err=%v", second, err)
	}
	if renewed, err := service.RenewNormalRouteLatencyProbeClaim(contextBackground(), *claim); err != nil || !renewed {
		t.Fatalf("renew = %v err=%v", renewed, err)
	}
	fast := int64(500)

	// First probe success opens the two-probe round.
	first, err := service.RecordNormalRouteRecoveryProbeSuccess(contextBackground(), account, candidates[0], &fast)
	if err != nil || first == nil || first.Cleared {
		t.Fatalf("first probe success: %+v err=%v", first, err)
	}
	if first.RecoverySuccessCount != 1 || first.RequiredRecoverySuccessCount != 2 {
		t.Fatalf("first probe counters: %+v", first)
	}

	// The state moved; the stale candidate must not drive the second probe.
	stale, err := service.RecordNormalRouteRecoveryProbeSuccess(contextBackground(), account, candidates[0], &fast)
	if err != nil || stale != nil {
		t.Fatalf("stale candidate: %+v err=%v", stale, err)
	}

	// Refresh candidates for the second probe of the round; every probe
	// result reschedules the next probe into the future.
	clock.Advance(5_002)
	candidates, err = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("refreshed candidates = %v err=%v", candidates, err)
	}
	second_, err := service.RecordNormalRouteRecoveryProbeSuccess(contextBackground(), account, candidates[0], &fast)
	if err != nil || second_ == nil || !second_.Cleared {
		t.Fatalf("second probe success: %+v err=%v", second_, err)
	}
	if second_.RecoverySuccessCount != 2 {
		t.Fatalf("second probe counters: %+v", second_)
	}
	if err := service.ReleaseNormalRouteLatencyProbeClaim(contextBackground(), *claim); err != nil {
		t.Fatal(err)
	}
}

func TestLatencyProbeFailureMixedPairDiscarded(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	fast := int64(500)

	// Round 1: success then failure = mixed pair, deliberately discarded.
	clock.Advance(5_002)
	candidates, _ := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if _, err := service.RecordNormalRouteRecoveryProbeSuccess(contextBackground(), account, candidates[0], &fast); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5_002)
	candidates, _ = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	failure, err := service.RecordNormalRouteProbeFailure(contextBackground(), candidates[0], "")
	if err != nil || failure == nil {
		t.Fatalf("probe failure: %+v err=%v", failure, err)
	}
	if failure.DegradedUntil == nil || *failure.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("mixed pair must not extend the lease: %+v", failure)
	}

	// Round 2: two failures renew the lease from the failure moment.
	clock.Advance(60_000)
	clock.Advance(5_002)
	candidates, _ = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if _, err := service.RecordNormalRouteProbeFailure(contextBackground(), candidates[0], ""); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5_002)
	finalFailureAtMs := clock.NowMs()
	candidates, _ = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	failure2, err := service.RecordNormalRouteProbeFailure(contextBackground(), candidates[0], "")
	if err != nil || failure2 == nil {
		t.Fatalf("second FF: %+v err=%v", failure2, err)
	}
	if failure2.DegradedUntil == nil || *failure2.DegradedUntil != ISOStringMs(finalFailureAtMs+120_000) {
		t.Fatalf("double failure must renew the lease: %+v (want %s)", failure2, ISOStringMs(finalFailureAtMs+120_000))
	}
	if failure2.SlowCount != 3 {
		t.Fatalf("probe failure slowCount floor = %d", failure2.SlowCount)
	}
}

func TestLatencyDeferAndDiscardCandidate(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(5_002)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %v err=%v", candidates, err)
	}

	deferred, err := service.DeferNormalRouteLatencyProbeCandidate(contextBackground(), candidates[0])
	if err != nil || !deferred {
		t.Fatalf("defer = %v err=%v", deferred, err)
	}
	// After the defer the next probe is in the future again.
	future := clock.NowMs() + 1
	early, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, &future)
	if err != nil || len(early) != 0 {
		t.Fatalf("deferred candidates surfaced early: %v err=%v", early, err)
	}

	// Discard removes the state entirely once the deferred probe is due again.
	clock.Advance(5_002)
	candidates, err = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates before discard = %d err=%v", len(candidates), err)
	}
	if err := service.DiscardNormalRouteLatencyProbeCandidate(contextBackground(), candidates[0]); err != nil {
		t.Fatal(err)
	}
	candidates, err = service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after discard = %v err=%v", candidates, err)
	}
}

func TestLatencyClearAllRotatesGeneration(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Publishing an older-or-equal event ('aaaa' sorts before 'initial') is a
	// no-op that only refreshes the marker TTL.
	clock.Advance(5_002)
	if _, err := service.ClearAllNormalRouteLatencyDegradation(contextBackground(), LatencyGenerationEvent{
		Version: "aaaa", PublishedAt: "1970-01-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("older-or-equal event must keep the state: %v err=%v", candidates, err)
	}

	// A newer event rotates the generation and invalidates all states.
	if _, err := service.ClearAllNormalRouteLatencyDegradation(contextBackground(), LatencyGenerationEvent{
		Version: "reset-2", PublishedAt: ISOStringMs(clock.NowMs() + 1),
	}); err != nil {
		t.Fatal(err)
	}
	if degraded, err := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), account, scope); err != nil || degraded {
		t.Fatalf("generation rotation must clear degradation: %v err=%v", degraded, err)
	}
}

func TestLatencyClearForRouteStrategyAndAccount(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	config := speedFirstConfig()
	scopeA := &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: "g1"}
	scopeB := &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs2", GroupID: "g1"}
	account := latencyAccount("a")
	for _, scope := range []*LatencyDegradationScope{scopeA, scopeB} {
		for i := 0; i < 3; i++ {
			if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
				t.Fatal(err)
			}
		}
	}

	cleared, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(contextBackground(), " rs1 ")
	if err != nil || cleared != 1 {
		t.Fatalf("route strategy clear = %d err=%v", cleared, err)
	}
	if degraded, _ := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), account, scopeA); degraded {
		t.Fatal("rs1 state must be cleared")
	}
	if degraded, _ := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), account, scopeB); !degraded {
		t.Fatal("rs2 state must survive")
	}

	bindingGroup := "g1"
	cleared, err = service.ClearNormalRouteLatencyDegradationForAccountBinding(contextBackground(),
		ClearNormalRouteLatencyDegradationForAccountBindingInput{
			SystemAccountID: "sys", AccountID: "a", GroupIDs: []*string{&bindingGroup, nil},
		})
	if err != nil || cleared != 1 {
		t.Fatalf("binding clear = %d err=%v", cleared, err)
	}

	// Recreate and clear at account level across scopes.
	for _, scope := range []*LatencyDegradationScope{scopeA, scopeB} {
		for i := 0; i < 3; i++ {
			if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	cleared, err = service.ClearNormalRouteLatencyDegradationForAccount(contextBackground(),
		ClearNormalRouteLatencyDegradationForAccountInput{SystemAccountID: "sys", AccountID: "a"})
	if err != nil || cleared != 2 {
		t.Fatalf("account clear = %d err=%v", cleared, err)
	}
}

func TestListNormalRouteLatencyDegradedRuntime(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	items, err := service.ListNormalRouteLatencyDegradedRuntime(contextBackground(), ListNormalRouteLatencyDegradedRuntimeInput{
		RouteStrategyIDs: []string{"rs1"},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	item := items[0]
	if item.AccountID != "a" || item.AccountName == nil || *item.AccountName != "Account a" {
		t.Fatalf("item identity = %+v", item)
	}
	if item.SlowTriggerCount != 3 || item.SlowWindowSeconds != 60 || item.RequiredRecoverySuccessCount != 3 {
		t.Fatalf("item config = %+v", item)
	}
	if item.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("item degradedUntil = %s", item.DegradedUntil)
	}

	if _, err := service.ListNormalRouteLatencyDegradedRuntime(contextBackground(), ListNormalRouteLatencyDegradedRuntimeInput{
		RouteStrategyIDs: []string{strings.Repeat("rs", 1)},
	}); err != nil {
		t.Fatalf("small query must not fail: %v", err)
	}

	ids := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		ids = append(ids, "rs"+string(rune('a'+i%26))+itoaForTest(int64(i)))
	}
	_, err = service.ListNormalRouteLatencyDegradedRuntime(contextBackground(), ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: ids})
	if err == nil || err.Error() != "普通路由速度优先运行态查询最多支持 50 个策略路由" {
		t.Fatalf("over-limit error = %v", err)
	}
}

func itoaForTest(value int64) string {
	digits := make([]byte, 0, 8)
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

func TestLatencyScopedKeySanitization(t *testing.T) {
	scope := LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs 1/x", GroupID: "g:1"}
	account := SuppressibleGatewayAccount{ID: "a"}
	key := accountLatencyStateKey(scope, account)
	if key != "v1:sys:rs_1_x:g:1:a" {
		t.Fatalf("key = %q", key)
	}
	// Nil scope helper rejects incomplete input.
	if NormalRouteLatencyDegradationScope("sys", " ", "g1") != nil {
		t.Fatal("blank strategy must yield nil scope")
	}
}
