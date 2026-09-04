package gatewaycircuit

import (
	"strings"
	"testing"
)

func newTestSuppressionStore(now func() int64, concurrency func(string) int, redisManaged bool) *LocalSuppressionStore {
	return NewLocalSuppressionStore(LocalSuppressionStoreOptions{
		Now:                now,
		AccountConcurrency: concurrency,
		CanUseProcessLocal: func() bool { return !redisManaged },
	})
}

func suppressibleAccount(id string) SuppressibleAccount {
	return SuppressibleAccount{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: id}}
}

func TestSuppressLadderAndPrecheckRequired(t *testing.T) {
	now := int64(0)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	first := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if first.Action != SuppressionActionSuppressed || first.LocalFailureCount != 1 || first.DelayMs != 3000 || !first.HasDelayMs {
		t.Fatalf("first = %+v", first)
	}
	if first.Until == "" {
		t.Fatalf("until must be an RFC3339 instant")
	}
	// Failing again while suppressed keeps the count but does not advance the
	// ladder.
	second := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if second.LocalFailureCount != 1 || second.DelayMs != 3000 {
		t.Fatalf("second = %+v", second)
	}
	// Once the suppression expired the ladder advances.
	now = 3100
	third := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if third.LocalFailureCount != 2 || third.DelayMs != 5000 {
		t.Fatalf("third = %+v", third)
	}
	now = 8200
	fourth := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if fourth.LocalFailureCount != 3 || fourth.DelayMs != 10_000 {
		t.Fatalf("fourth = %+v", fourth)
	}
	// After the ladder: too short an observation delays the precheck.
	now = 14_000
	store.AgeSuppressionSinceForTest("acc", 14_000-5_000)
	fifth := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if fifth.Action != SuppressionActionSuppressed || fifth.DelayMs != 10_000 {
		t.Fatalf("fifth = %+v", fifth)
	}
	// A long observation period requires the precheck.
	now = 80_000
	store.AgeSuppressionSinceForTest("acc", 19_000)
	sixth := store.SuppressForGatewayFailure("acc", "acc", "transport:boom", "")
	if sixth.Action != SuppressionActionPrecheckRequired || sixth.HasDelayMs {
		t.Fatalf("sixth = %+v", sixth)
	}
	if store.CountVisibleSuppressions(func(string) bool { return false }) != 1 {
		t.Fatalf("visible suppressions = %d", store.CountVisibleSuppressions(func(string) bool { return false }))
	}
}

func TestFilterSuppressionsAndHalfOpenLease(t *testing.T) {
	now := int64(0)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	store.SuppressForGatewayFailure("suppressed", "suppressed", "transport:boom", "")
	accounts := []SuppressibleAccount{suppressibleAccount("clean"), suppressibleAccount("suppressed")}
	neverBlocking := func(string) bool { return false }
	result := store.FilterSuppressions(accounts, neverBlocking, SuppressionFilterOptions{})
	if result.SuppressedCount != 1 || len(result.Accounts) != 1 || result.Accounts[0].ID != "clean" {
		t.Fatalf("filter result = %+v", result)
	}
	if result.AllSuppressed {
		t.Fatalf("allSuppressed must be false")
	}
	if result.NextRetryAtMs == nil || *result.NextRetryAtMs != 3000 {
		t.Fatalf("nextRetryAtMs = %v", result.NextRetryAtMs)
	}
	if result.NextRetryAfterMs == nil || *result.NextRetryAfterMs != 3000 {
		t.Fatalf("nextRetryAfterMs = %v", result.NextRetryAfterMs)
	}
	// All suppressed: the wait window metadata flips.
	onlySuppressed := []SuppressibleAccount{suppressibleAccount("suppressed")}
	allResult := store.FilterSuppressions(onlySuppressed, neverBlocking, SuppressionFilterOptions{})
	if !allResult.AllSuppressed || len(allResult.Accounts) != 0 {
		t.Fatalf("all suppressed result = %+v", allResult)
	}
	// An expired suppression can acquire a half-open lease.
	now = 3001
	leaseResult := store.FilterSuppressions(onlySuppressed, neverBlocking, SuppressionFilterOptions{AcquireHalfOpenLease: true})
	if len(leaseResult.Accounts) != 1 || len(leaseResult.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("lease result = %+v", leaseResult)
	}
	lease := leaseResult.AcquiredHalfOpenLeases[0]
	if lease.Release() != true {
		t.Fatalf("release must succeed once")
	}
	if lease.Release() {
		t.Fatalf("second release must fail")
	}
	// While half_open with active concurrency, the account stays blocked.
	now = 3100
	concurrency := 1
	store2 := newTestSuppressionStore(func() int64 { return now }, func(string) int { return concurrency }, false)
	store2.Suppress("acc", 1000, "r", AvailabilityStatusHalfOpen, &suppressionMetadata{
		accountID: "acc", accountConcurrencyAccountID: "acc",
		halfOpenLeaseUntilMs: int64Ptr(now + 500), halfOpenLeaseID: strPtr("lease-1"),
	})
	blockedResult := store2.FilterSuppressions([]SuppressibleAccount{suppressibleAccount("acc")}, neverBlocking, SuppressionFilterOptions{})
	if blockedResult.SuppressedCount != 1 {
		t.Fatalf("half-open with concurrency must block: %+v", blockedResult)
	}
	if blockedResult.NextRetryAtMs == nil || *blockedResult.NextRetryAtMs != now+1000 {
		t.Fatalf("visible until with concurrency = %v", blockedResult.NextRetryAtMs)
	}
	// An expired half-open lease with no concurrency is reacquirable.
	concurrency = 0
	now = 3700
	acquirable := store2.FilterSuppressions([]SuppressibleAccount{suppressibleAccount("acc")}, neverBlocking, SuppressionFilterOptions{AcquireHalfOpenLease: true})
	if len(acquirable.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("expired half-open lease must be reacquirable: %+v", acquirable)
	}
}

func TestSuppressionDegradationActivation(t *testing.T) {
	now := int64(0)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	first := store.DegradeForGatewayFailure("acc", "acc", "transport:boom")
	if first.Status != AvailabilityStatusNormal || first.FailureCount == nil || *first.FailureCount != 1 {
		t.Fatalf("first = %+v", first)
	}
	// Second failure inside the window but below the observation floor.
	now = 1000
	second := store.DegradeForGatewayFailure("acc", "acc", "transport:boom")
	if second.Status != AvailabilityStatusNormal || *second.FailureCount != 2 {
		t.Fatalf("second = %+v", second)
	}
	// Past the minimum observation the degradation activates.
	now = 61_000
	third := store.DegradeForGatewayFailure("acc", "acc", "transport:boom")
	if third.Status != AvailabilityStatusDegraded || *third.FailureCount != 3 {
		t.Fatalf("third = %+v", third)
	}
	// Ordering places degraded accounts after normal ones and preserves tiers.
	accounts := []SuppressibleAccount{suppressibleAccount("acc"), suppressibleAccount("b"), suppressibleAccount("c")}
	order := store.OrderDegradations(accounts, nil)
	if !order.Applied || order.DegradedCount != 1 || order.Accounts[0].ID != "b" {
		t.Fatalf("order = %+v", order)
	}
	if store.CountDegradations() != 1 {
		t.Fatalf("degradations = %d", store.CountDegradations())
	}
	// Active degradations persist (Node only cleans inactive ones whose
	// window elapsed).
	now = 61_000 + 5*60_000 + 1
	if count := store.CountDegradations(); count != 1 {
		t.Fatalf("active degradations = %d", count)
	}
	// An inactive degradation is dropped once its window elapses.
	single := newTestSuppressionStore(func() int64 { return now }, nil, false)
	single.DegradeForGatewayFailure("once", "once", "r")
	// CountDegradations only counts active degradations (Node semantics).
	if single.CountDegradations() != 0 {
		t.Fatalf("inactive degradation must not be counted")
	}
	// Backend-activated degradation starts active.
	availability := store.ActivateRuntimeDegradation("acc2", "acc2", "probe failed", nil, nil)
	if availability.Status != AvailabilityStatusDegraded {
		t.Fatalf("activated = %+v", availability)
	}
}

func TestSuppressionRedisManaged(t *testing.T) {
	now := int64(0)
	store := newTestSuppressionStore(func() int64 { return now }, nil, true)
	result := store.SuppressForGatewayFailure("acc", "acc", "r", "")
	if result.Action != SuppressionActionRedisManaged || result.LocalFailureCount != 0 {
		t.Fatalf("result = %+v", result)
	}
	if availability := store.DegradeForGatewayFailure("acc", "acc", "r"); availability.Status != AvailabilityStatusNormal {
		t.Fatalf("degrade under redis = %+v", availability)
	}
	if snapshot := store.SnapshotAvailability(func(string) bool { return false }); len(snapshot) != 0 {
		t.Fatalf("snapshot under redis = %+v", snapshot)
	}
	if filtered := store.FilterSuppressions([]SuppressibleAccount{suppressibleAccount("acc")}, func(string) bool { return false }, SuppressionFilterOptions{}); filtered.SuppressedCount != 0 || len(filtered.Accounts) != 1 {
		t.Fatalf("filter under redis = %+v", filtered)
	}
}

func TestDispatchPriorityTierPreservation(t *testing.T) {
	// Tiers: modelRank:fallback:super:priority.
	make := func(id string, priority int64, super, fallback bool) SuppressibleAccount {
		return SuppressibleAccount{
			SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: id},
			Priority: priority, SuperPriorityEnabled: super, FallbackEnabled: fallback,
		}
	}
	base := []SuppressibleAccount{make("a", 10, true, false), make("b", 10, false, false)}
	reordered := []SuppressibleAccount{make("b", 10, false, false), make("a", 10, true, false)}
	out := preserveDispatchPriorityTiers(base, reordered, nil)
	if out[0].ID != "a" || out[1].ID != "b" {
		t.Fatalf("tier order not preserved: %v", out)
	}
	if tier := DispatchPriorityTier(make("a", 3, true, true), map[string]int64{"a": 2}); tier != "2:1:0:3" {
		t.Fatalf("tier = %s", tier)
	}
	// Unknown model ranks fall back to rank 3.
	if tier := DispatchPriorityTier(make("z", 0, false, false), map[string]int64{}); !strings.HasPrefix(tier, "3:0:1:") {
		t.Fatalf("unknown tier = %s", tier)
	}
	// Without a rank map the rank is 0.
	if tier := DispatchPriorityTier(make("z", 0, false, false), nil); !strings.HasPrefix(tier, "0:0:1:") {
		t.Fatalf("no-map tier = %s", tier)
	}
}

func TestRuntimeKeyBuilding(t *testing.T) {
	key, err := GatewayAccountRuntimeKey(SuppressibleGatewayAccount{
		ID: "acc", AccessType: "authorized",
		BindingSystemAccountID: "sys", BoundGroupID: "grp", AccountAuthorizationID: "auth",
	})
	if err != nil || key != "acc:authorized:sys:grp:auth" {
		t.Fatalf("key = (%s, %v)", key, err)
	}
	if _, err := GatewayAccountRuntimeKey(SuppressibleGatewayAccount{ID: "acc", AccessType: "authorized"}); err == nil ||
		err.Error() != "授权账户运行态键缺少绑定上下文" {
		t.Fatalf("missing binding error = %v", err)
	}
	if RuntimeAccountIDFromKey("acc:authorized:sys") != "acc" || RuntimeAccountIDFromKey("acc") != "acc" {
		t.Fatalf("runtime account id extraction failed")
	}
	if GatewayAccountConcurrencyAccountID("acc", "source") != "source" || GatewayAccountConcurrencyAccountID("acc", "") != "acc" {
		t.Fatalf("concurrency account id failed")
	}
}

func TestLocalSuppressionExhaustedContract(t *testing.T) {
	failure := LocalSuppressionExhaustedFailureResponse(int64Ptr(1500))
	if failure.StatusCode != 503 {
		t.Fatalf("status = %d", failure.StatusCode)
	}
	if failure.Message != "所有上游账户正在临时隔离，请稍后重试" {
		t.Fatalf("message = %s", failure.Message)
	}
	if failure.ErrorType != "service_unavailable" || failure.ErrorCode != "upstream_retryable_error" || failure.ErrorPhase != "dispatch" {
		t.Fatalf("error contract = %+v", failure)
	}
	if failure.RetryAfterMs == nil || *failure.RetryAfterMs != 1500 {
		t.Fatalf("retryAfter = %+v", failure.RetryAfterMs)
	}
	if key := RecoverableSuppressionScopeKey("sys", "", "grp"); key != "sys::grp" {
		t.Fatalf("scope key = %s", key)
	}
	if key := RecoverableSuppressionScopeKey("sys", "key", "grp"); key != "sys:key:grp" {
		t.Fatalf("scope key = %s", key)
	}
}
