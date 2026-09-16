package gatewaysession

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// lazyRedisClient full surface against a real miniredis.
// ---------------------------------------------------------------------------

func TestW9CLazyRedisClientSurface(t *testing.T) {
	server := miniredis.RunT(t)
	lazy := newLazyRedisClient("redis://" + server.Addr())
	ctx := context.Background()

	if value, err := lazy.Get(ctx, "missing"); err != nil || value != nil {
		t.Fatalf("lazy get = %v, %v", value, err)
	}
	if err := lazy.SetPX(ctx, "k", "v", 60_000); err != nil {
		t.Fatalf("lazy setpx: %v", err)
	}
	if value, err := lazy.Get(ctx, "k"); err != nil || value == nil || *value != "v" {
		t.Fatalf("lazy get after set = %v, %v", value, err)
	}
	result, err := lazy.Eval(ctx, "return 1", []string{})
	if err != nil || result == nil {
		t.Fatalf("lazy eval = %v, %v", result, err)
	}
	if pong, err := lazy.SendCommand(ctx, "PING"); err != nil || pong != "PONG" {
		t.Fatalf("lazy ping = %v, %v", pong, err)
	}
	if err := lazy.Del(ctx, "k"); err != nil {
		t.Fatalf("lazy del: %v", err)
	}
	if err := lazy.Close(); err != nil {
		t.Fatalf("lazy close: %v", err)
	}
	// A second dial happens transparently after Close.
	if err := lazy.SetPX(ctx, "k2", "v2", 1000); err != nil {
		t.Fatalf("lazy re-dial: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Redis driver affinity flows (binding lifecycle, migrate, traffic
// preference, async ordering).
// ---------------------------------------------------------------------------

func TestW9CRedisDriverRememberForgetAndMigrate(t *testing.T) {
	svc, clock, logger, mr := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// Sync remember under the redis driver is fire-and-forget: wait for the
	// goroutine to land the binding in miniredis.
	svc.RememberOpenAIAccountForSession("w9c-aff-1", "acc-1", scope)
	deadline := time.Now().Add(3 * time.Second)
	bound := ""
	for time.Now().Before(deadline) {
		if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); ok && owner == "acc-1" {
			bound = owner
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if bound != "acc-1" {
		t.Fatalf("redis binding never landed: %q", bound)
	}
	// Re-claiming the same account refreshes (still owner).
	if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); !ok || owner != "acc-1" {
		t.Fatalf("re-claim = %q %v", owner, ok)
	}
	// A different account cannot steal the binding: the claim reports the
	// current owner (first binder wins).
	if owner, _ := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-2", scope); owner != "acc-1" {
		t.Fatalf("steal must report the existing owner, got %q", owner)
	}

	// The sync forget under the redis driver is refused with a warn.
	svc.ForgetOpenAIAccountForSession("w9c-aff-1", "acc-1")
	if !logger.HasEvent("redis_openai_session_affinity_sync_forget_ignored") {
		t.Fatalf("sync forget must warn, events = %v", logger.Events())
	}
	// The async forget removes the binding.
	if err := svc.ForgetOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1"); err != nil {
		t.Fatalf("async forget: %v", err)
	}
	if _, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-1", "acc-1", scope); !ok {
		t.Fatal("binding must be re-claimable after forget")
	}

	// Migration moves every binding of the source account to the target.
	svc.RememberOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-m", scope)
	svc.RememberOpenAIAccountForSessionAsync(ctx, "w9c-aff-m2", "acc-m", scope)
	result, err := svc.MigrateOpenAIAccountSessionAffinityAsync(ctx, "acc-m", "acc-mt", scope, MigrationOptions{PreferMigratedSessions: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.MigratedSessionCount != 2 {
		t.Fatalf("migrated = %d", result.MigratedSessionCount)
	}
	if owner, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-mt", scope); !ok || owner != "acc-mt" {
		t.Fatalf("migrated binding owner = %q %v", owner, ok)
	}

	// TTL expiry drops the redis binding.
	clock.Advance(time.Duration(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs) * time.Millisecond)
	mr.FastForward(time.Duration(sessionAffinityTtlMs) * time.Millisecond)
	if _, ok := svc.ClaimOpenAIAccountForSessionAsync(ctx, "w9c-aff-m1", "acc-mt", scope); !ok {
		t.Fatal("binding must be re-claimable after ttl expiry")
	}
}

func TestW9CRedisDriverTrafficMigrationPreference(t *testing.T) {
	svc, clock, _, mr := newRedisAffinityService(t)
	ctx := context.Background()
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	// Async write with ThrowOnRedisError succeeds against miniredis.
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-a", "acc-b", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err != nil {
		t.Fatalf("redis preference write: %v", err)
	}
	preference := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope)
	if preference == nil || preference.TargetAccountID != "acc-b" {
		t.Fatalf("redis preference = %+v", preference)
	}
	// Source present consumes and deletes the redis preference.
	if consumed := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-a", "acc-b"}, scope); consumed != nil {
		t.Fatalf("consumed preference = %+v", consumed)
	}
	if again := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope); again != nil {
		t.Fatalf("preference must stay deleted: %+v", again)
	}

	// Corrupted payload degrades to nil (and is cleaned up).
	svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-a", "acc-b", scope, TrafficMigrationPreferenceWriteOptions{})
	key, keyErr := redisTrafficMigrationPreferenceKey(svc.cfg.RedisNamespace, "sys-1:*:grp-1")
	if keyErr != nil {
		t.Fatalf("preference key: %v", keyErr)
	}
	if err := mr.Set(key, "{corrupted"); err != nil {
		t.Fatalf("seed corruption: %v", err)
	}
	if corrupted := svc.trafficMigrationPreferenceForAccountsAsync(ctx, []string{"acc-b", "acc-x"}, scope); corrupted != nil {
		t.Fatalf("corrupted preference = %+v", corrupted)
	}
	if raw, getErr := mr.Get(key); getErr == nil && raw != "" {
		t.Fatalf("corrupted preference must be deleted, got %q", raw)
	}

	// Redis outage surfaces through ThrowOnRedisError only.
	mr.Close()
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-c", "acc-d", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err == nil {
		t.Fatal("outage must surface with ThrowOnRedisError")
	}
	// Without the flag the write failure only warns.
	if err := svc.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, "acc-c", "acc-d", scope, TrafficMigrationPreferenceWriteOptions{}); err != nil {
		t.Fatalf("outage without throw = %v", err)
	}
	_ = clock
}

func TestW9CRedisDriverAsyncOrderingAndBusyLanes(t *testing.T) {
	svc, _, _, _ := newRedisAffinityService(t)
	ctx := context.Background()
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		testAccount("a", 1, nil),
		testAccount("b", 2, nil),
	}

	// High-concurrency ordering through the redis runtime driver exercises
	// the async in-flight stats + candidate sorter.
	ordered, err := svc.OrderOpenAIAccountsBySessionAffinityAsync(ctx, accounts, "aff-w9c", DispatchOrderingOptions{
		GroupType:        GroupTypeHighConcurrency,
		SchedulingPolicy: map[string]any{},
	})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("redis async high concurrency ordering = %v, %v", accountsIDs(ordered), err)
	}

	// Personal ordering via a session binding carrying the migration flag.
	binding := &SessionBinding{AccountID: "b", Scope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"}, TrafficMigrationPreferred: true}
	if target := sessionTrafficMigrationTargetForAccountsFromBinding(accountsIDs(accounts), binding); target != "b" {
		t.Fatalf("binding migration target = %q", target)
	}
	if target := sessionTrafficMigrationTargetForAccountsFromBinding([]string{"a", "c"}, binding); target != "" {
		t.Fatalf("unrelated binding target = %q", target)
	}

	// Async busy-lane check with the redis runtime driver.
	busyOpts := BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}, ""}
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOpts); err != nil || busy {
		t.Fatalf("async busy = %v, %v", busy, err)
	}
	// Image lane via the async path with per-lane stats.
	concurrency := svc.cfg.Concurrency.(*mockConcurrency)
	concurrency.SetLane(RequestLaneImage, "a", 99)
	concurrency.SetLane(RequestLaneImage, "b", 99)
	imageOpts := BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency}, RequestLaneImage}
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, imageOpts); err != nil || !busy {
		t.Fatalf("async image busy = %v, %v", busy, err)
	}
	// Runtime stat at/over the hard limit reports busy.
	concurrency.SetCurrent("a", 5)
	concurrency.SetCurrent("b", 5)
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOpts); err != nil || !busy {
		t.Fatalf("async hard busy = %v, %v", busy, err)
	}
	// Failing concurrency source surfaces the error.
	failSvc, _, _, _ := newRedisAffinityService(t)
	failSvc.cfg.Concurrency = w9cFailingConcurrency{}
	if _, err := failSvc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOpts); err == nil {
		t.Fatal("failing concurrency source must error")
	}
	// Non-high-concurrency groups under the redis runtime driver short-circuit.
	if busy, err := svc.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, BusyLaneOptions{DispatchOrderingOptions{GroupType: GroupTypePersonal}, ""}); err != nil || busy {
		t.Fatalf("personal async busy = %v, %v", busy, err)
	}
}

type w9cFailingConcurrency struct{}

func (w9cFailingConcurrency) GetAccountCurrentConcurrency(string, string) int { return 0 }
func (w9cFailingConcurrency) LoadAccountCurrentConcurrencyByIDsAsync(context.Context, []string, string) (map[string]int, error) {
	return nil, errors.New("concurrency boom")
}
func (w9cFailingConcurrency) LoadAccountInFlightStatsByIDs([]string, InFlightThresholds) map[string]AccountInFlightStats {
	return map[string]AccountInFlightStats{}
}
func (w9cFailingConcurrency) LoadAccountInFlightStatsByIDsAsync(context.Context, []string, InFlightThresholds) (map[string]AccountInFlightStats, error) {
	return nil, errors.New("in-flight boom")
}

// ---------------------------------------------------------------------------
// compareHighConcurrencyCandidates deep tie-breakers.
// ---------------------------------------------------------------------------

func TestW9CCompareHighConcurrencyCandidatesTieBreakers(t *testing.T) {
	policy := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{GlobalMax: 100})
	mk := func(id string, mutate func(*highConcurrencyCandidate)) highConcurrencyCandidate {
		candidate := highConcurrencyCandidate{
			account: testAccount(id, 3, nil), index: 0, softLimit: 10, hardLimit: 10,
		}
		if mutate != nil {
			mutate(&candidate)
		}
		return candidate
	}

	// Soft busy loses; super priority wins; smaller priority value wins.
	// Soft busy loses (ranked after a non-busy peer).
	softBusy := mk("soft", func(c *highConcurrencyCandidate) { c.softBusy = true })
	if compareHighConcurrencyCandidates(softBusy, mk("free", nil), &policy, false, nil) <= 0 {
		t.Fatal("soft busy must lose")
	}
	super := mk("super", func(c *highConcurrencyCandidate) { c.account.SuperPriorityEnabled = true })
	if compareHighConcurrencyCandidates(super, mk("plain", nil), &policy, false, nil) >= 0 {
		t.Fatal("super priority must win")
	}
	lowerPriority := mk("low", func(c *highConcurrencyCandidate) { c.account.Priority = 1 })
	if compareHighConcurrencyCandidates(lowerPriority, mk("high", nil), &policy, false, nil) >= 0 {
		t.Fatal("smaller priority value must win")
	}
	// Load ratio tie-break.
	lighter := mk("light", func(c *highConcurrencyCandidate) { c.currentConcurrency = 1 })
	heavier := mk("heavy", func(c *highConcurrencyCandidate) { c.currentConcurrency = 8 })
	if compareHighConcurrencyCandidates(lighter, heavier, &policy, false, nil) >= 0 {
		t.Fatal("lighter load must win")
	}
	// Raw concurrency, slow counters and oldest in-flight tie-breaks.
	fewer := mk("few", func(c *highConcurrencyCandidate) { c.currentConcurrency = 5; c.softLimit = 5 })
	more := mk("more", func(c *highConcurrencyCandidate) { c.currentConcurrency = 7; c.softLimit = 5 })
	if compareHighConcurrencyCandidates(fewer, more, &policy, false, nil) >= 0 {
		t.Fatal("lower raw concurrency must win")
	}
	lessSlow := mk("fast", func(c *highConcurrencyCandidate) { c.firstOutputSlowCount = 0 })
	moreSlow := mk("slow", func(c *highConcurrencyCandidate) { c.firstOutputSlowCount = 3 })
	if compareHighConcurrencyCandidates(lessSlow, moreSlow, &policy, false, nil) >= 0 {
		t.Fatal("fewer first-output-slow must win")
	}
	older := mk("old", func(c *highConcurrencyCandidate) { c.slowInFlightCount = 2 })
	newer := mk("new", func(c *highConcurrencyCandidate) { c.slowInFlightCount = 0 })
	if compareHighConcurrencyCandidates(newer, older, &policy, false, nil) >= 0 {
		t.Fatal("fewer slow in-flight must win")
	}
	olderMs := mk("oldms", func(c *highConcurrencyCandidate) { c.oldestInFlightMs = 9_000 })
	newerMs := mk("newms", func(c *highConcurrencyCandidate) { c.oldestInFlightMs = 1_000 })
	if compareHighConcurrencyCandidates(newerMs, olderMs, &policy, false, nil) >= 0 {
		t.Fatal("younger oldest-in-flight must win")
	}
	// affinityAllowed wins; finally the input index keeps ordering stable.
	allowed := mk("allowed", func(c *highConcurrencyCandidate) { c.affinityAllowed = true })
	notAllowed := mk("not", func(c *highConcurrencyCandidate) { c.affinityAllowed = false })
	if compareHighConcurrencyCandidates(allowed, notAllowed, &policy, false, nil) >= 0 {
		t.Fatal("affinity-allowed must win")
	}
	first := mk("first", nil)
	second := mk("second", nil)
	second.index = 1
	if compareHighConcurrencyCandidates(first, second, &policy, false, nil) > 0 {
		t.Fatal("index must keep stable order")
	}
	// FallbackOnQueueEnabled with no primary soft available keeps reserve
	// (fallback-enabled) accounts behind the primary accounts.
	queuePolicy := policy
	queuePolicy.FallbackOnQueueEnabled = true
	fallbackBusy := mk("fb-busy", func(c *highConcurrencyCandidate) {
		c.account.FallbackEnabled = true
		c.softBusy = true
	})
	primaryBusy := mk("primary-busy", func(c *highConcurrencyCandidate) { c.softBusy = true })
	if compareHighConcurrencyCandidates(primaryBusy, fallbackBusy, &queuePolicy, false, nil) >= 0 {
		t.Fatal("queue mode must rank primary accounts ahead of fallback reserves")
	}
}
