package gatewaycircuit

// w11c: wait coordinator/engine branches, local suppression visibility,
// degradation ordering and small helpers.

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

func TestW11CWaitCoordinatorDefaults(t *testing.T) {
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{})
	if coordinator.newTimer == nil || coordinator.now == nil || coordinator.maxWaitersPerScope < 1 || coordinator.maxWaitersGlobal < 1 {
		t.Fatalf("defaults not applied: %+v", coordinator)
	}
	if coordinatorScopeKey("r", "s") != `["r","s"]` {
		t.Fatalf("scope key encoding changed")
	}
}

func TestW11CWaitCoordinatorSettlesNonHeadAndDeadline(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	// Register two waiters on the same scope; the head blocks the queue.
	first := make(chan string, 1)
	second := make(chan string, 1)
	go func() { first <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "w11c", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000}) }()
	for coordinator.Snapshot().WaiterCount < 1 {
		time.Sleep(time.Millisecond)
	}
	go func() { second <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "w11c", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000}) }()
	for coordinator.Snapshot().WaiterCount < 2 {
		time.Sleep(time.Millisecond)
	}
	// Notify settles the head only; the second waiter stays queued.
	if !coordinator.NotifyOne("w11c", "r") {
		t.Fatalf("notify should settle the head")
	}
	if result := <-first; result != TurnReady {
		t.Fatalf("head turn = %s", result)
	}
	// The head's timer fires past its deadline: the second waiter resolves
	// with deadline_exceeded once the clock catches up.
	hub.clock = 3_000_000
	hub.fireAll()
	select {
	case result := <-second:
		if result != TurnDeadlineExceeded {
			t.Fatalf("second turn = %s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second waiter never settled")
	}
}

func TestW11CWaitCoordinatorSettleReadyPastDeadline(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	done := make(chan string, 1)
	go func() {
		done <- coordinator.WaitForTurn(WaitTurnInput{ScopeKey: "w11c-deadline", Reason: "r", DelayMs: 60_000, DeadlineAtMs: 2_000_000})
	}()
	for coordinator.Snapshot().WaiterCount < 1 {
		time.Sleep(time.Millisecond)
	}
	// Advance past the deadline then notify: the waiter resolves as
	// deadline_exceeded rather than ready.
	hub.clock = 5_000_000
	if !coordinator.NotifyOne("w11c-deadline", "r") {
		t.Fatalf("notify failed")
	}
	if result := <-done; result != TurnDeadlineExceeded {
		t.Fatalf("turn = %s", result)
	}
}

func TestW11CWaitNormalizeHelpers(t *testing.T) {
	if normalizePositiveMsInt(0, 5) != 5 || normalizePositiveMsInt(-3, 5) != 1 || normalizePositiveMsInt(7, 5) != 7 {
		t.Fatalf("normalizePositiveMsInt broken")
	}
	if normalizeNonNegativeMs(-2) != 0 || normalizeNonNegativeMs(9) != 9 {
		t.Fatalf("normalizeNonNegativeMs broken")
	}
	if normalizePositiveMs64(0) != 1 || normalizePositiveMs64(5) != 5 {
		t.Fatalf("normalizePositiveMs64 broken")
	}
	if normalizeDeadlineAtMs(42, 0) != 42 {
		t.Fatalf("normalizeDeadlineAtMs changed the value")
	}
	if int64Max64(1, 2) != 2 || int64Max64(3, 2) != 3 {
		t.Fatalf("int64Max64 broken")
	}
}

func TestW11CWaitEngineWallBudgetAndReserve(t *testing.T) {
	budgetMs := int64(10_000)
	wall, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 0, BudgetMs: &budgetMs, Now: func() int64 { return 0 },
	}, nil)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	clock := &fakeTimerClock{nowMs: 0}
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{NewTimer: clock.newTimer, Now: clock.now})
	reserve := int64(1_000)
	outcome, err := waitForRecoverableUnavailableState(context.Background(), waitInput{
		reason: "r", scopeKey: "w11c-scope",
		refresh:            func(context.Context) error { return nil },
		isReady:            func() bool { return false },
		nextRetryAfterMs:   func() (int64, bool) { return 0, false },
		maxWaitMs:          60_000, checkIntervalMs: 100,
		coordinator:              coordinator,
		gatewayRequestWallBudget: wall,
		finalResponseReserveMs:   &reserve,
		deadlineAtMs:             int64Ptr(500),
		now:                      clock.now,
	})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if outcome.ready || outcome.skippedReason == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestW11CWaitEngineBudgetExhaustion(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	budget, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: "w11c-req", BudgetMs: int64Ptr(10), Now: func() int64 { return hub.clock },
	})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	// Burn the whole budget through the public transition API so the wait
	// engine sees an exhausted budget on its first loop iteration.
	snapshot := budget.Snapshot(hub.clock)
	burn := func(token string, version int, atMs int64) {
		if _, transitionErr := budget.BeginWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
			WaitToken: token, ExpectedVersion: version, NowMs: int64Ptr(atMs),
		}); transitionErr != nil {
			t.Fatalf("begin %s: %v", token, transitionErr)
		}
		if _, transitionErr := budget.PauseWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
			WaitToken: token, ExpectedVersion: version + 1, NowMs: int64Ptr(atMs + 100),
		}); transitionErr != nil {
			t.Fatalf("pause %s: %v", token, transitionErr)
		}
	}
	burn("w11c-burn", snapshot.Version, hub.clock)
	done := make(chan waitOutcome, 1)
	go func() {
		outcome, _ := waitForRecoverableUnavailableState(context.Background(), waitInput{
			reason: "r", scopeKey: "w11c-scope",
			refresh:              func(context.Context) error { return nil },
			isReady:              func() bool { return false },
			nextRetryAfterMs:     func() (int64, bool) { return 0, false },
			waitWithoutRetryAfter: true,
			maxWaitMs:            60_000, checkIntervalMs: 100,
			coordinator:             coordinator,
			routeCoordinationBudget: budget,
			now:                     func() int64 { return hub.clock },
		})
		done <- outcome
	}()
	select {
	case outcome := <-done:
		if outcome.skippedReason != WaitSkippedBudgetExhausted {
			t.Fatalf("outcome = %+v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("wait never returned")
	}
}

func TestW11CWaitEngineAuditCapture(t *testing.T) {
	clock := &fakeTimerClock{nowMs: 0}
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{NewTimer: clock.newTimer, Now: clock.now})
	capture := &w11cMetadataCapture{}
	// An immediate deadline skip still records the scheduled metadata.
	outcome, err := waitForRecoverableUnavailableState(context.Background(), waitInput{
		reason: "r", scopeKey: "w11c-scope",
		refresh:          func(context.Context) error { return nil },
		isReady:          func() bool { return false },
		nextRetryAfterMs: func() (int64, bool) { return 0, false },
		maxWaitMs:        60_000, checkIntervalMs: 100,
		deadlineAtMs:     int64Ptr(0),
		coordinator:      coordinator,
		auditCapture:     capture,
		now:              clock.now,
	})
	if err != nil || outcome.skippedReason != WaitSkippedDeadlineExceeded {
		t.Fatalf("outcome = (%+v, %v)", outcome, err)
	}
	if len(capture.entries) == 0 {
		t.Fatalf("audit metadata not captured")
	}
}

type w11cMetadataCapture struct {
	entries []struct {
		label    string
		metadata map[string]any
	}
}

func (c *w11cMetadataCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	c.entries = append(c.entries, struct {
		label    string
		metadata map[string]any
	}{label, metadata})
}

func TestW11CSuppressionDefaultsWithoutInjection(t *testing.T) {
	store := NewLocalSuppressionStore(LocalSuppressionStoreOptions{})
	if store.now == nil || store.canUseProcessLocal == nil || store.accountConcurrency == nil || store.logger == nil {
		t.Fatalf("defaults not applied: %+v", store)
	}
	// Defaults keep the store usable.
	if result := store.SuppressForGatewayFailure("w11c-acc", "w11c-acc", "r", ""); result.Action != SuppressionActionSuppressed {
		t.Fatalf("suppress = %+v", result)
	}
}

func TestW11CSuppressionVisibilityBranches(t *testing.T) {
	now := int64(10_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	// An active suppression is visible in the snapshot.
	store.SuppressForGatewayFailure("w11c-acc", "w11c-acc", "r", "")
	snapshot := store.SnapshotAvailability(func(string) bool { return false })
	if entry, visible := snapshot["w11c-acc"]; !visible || entry.Status != AvailabilityStatusLocalSuppressed {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// Once the suppression lapses it is no longer visible.
	now = 10 * 60_000
	snapshot = store.SnapshotAvailability(func(string) bool { return false })
	if _, visible := snapshot["w11c-acc"]; visible {
		t.Fatalf("lapsed suppression should be hidden: %+v", snapshot)
	}
	// An active degradation surfaces for keys without a suppression; a
	// precheck-blocked runtime key stays hidden.
	now = 100_000
	store.DegradeForGatewayFailure("w11c-deg", "w11c-deg", "r")
	now = 200_000
	store.DegradeForGatewayFailure("w11c-deg", "w11c-deg", "r")
	now = 300_000
	store.DegradeForGatewayFailure("w11c-deg", "w11c-deg", "r")
	blocked := map[string]bool{"w11c-deg": true}
	snapshot = store.SnapshotAvailability(func(key string) bool { return blocked[key] })
	if _, visible := snapshot["w11c-deg"]; visible {
		t.Fatalf("precheck-blocked degradation should be hidden: %+v", snapshot)
	}
	snapshot = store.SnapshotAvailability(func(string) bool { return false })
	if entry, visible := snapshot["w11c-deg"]; !visible || entry.Status != AvailabilityStatusDegraded {
		t.Fatalf("degradation snapshot = %+v", snapshot)
	}
	// An inactive degradation (single failure) stays hidden.
	now = 400_000
	store.DegradeForGatewayFailure("w11c-once", "w11c-once", "r")
	snapshot = store.SnapshotAvailability(func(string) bool { return false })
	if _, visible := snapshot["w11c-once"]; visible {
		t.Fatalf("inactive degradation should be hidden: %+v", snapshot)
	}
}

func TestW11CSuppressionOrderDegradationsBranches(t *testing.T) {
	now := int64(10_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	accounts := []SuppressibleAccount{suppressibleAccount("a"), suppressibleAccount("b")}
	// No degradations: the input order is returned untouched.
	result := store.OrderDegradations(accounts, nil)
	if result.Applied || len(result.Accounts) != 2 {
		t.Fatalf("no-degradation order = %+v", result)
	}
	// An active degradation reorders the account behind the healthy one
	// (activation needs the min observation window between failures).
	for i := 0; i < 3; i++ {
		now += 120_000
		store.DegradeForGatewayFailure("a", "a", "r")
	}
	result = store.OrderDegradations(accounts, nil)
	if !result.Applied || result.DegradedCount != 1 || result.Accounts[0].ID != "b" || result.Accounts[1].ID != "a" {
		t.Fatalf("degraded order = %+v", result)
	}
	// All degraded: bypass flag set, order unchanged.
	for i := 0; i < 3; i++ {
		now += 120_000
		store.DegradeForGatewayFailure("b", "b", "r")
	}
	result = store.OrderDegradations(accounts, nil)
	if result.BypassedAllDegraded != true || result.Applied {
		t.Fatalf("all degraded = %+v", result)
	}
	// Redis-managed mode returns the accounts untouched.
	redis := newTestSuppressionStore(func() int64 { return now }, nil, true)
	result = redis.OrderDegradations(accounts, nil)
	if result.Applied || len(result.Accounts) != 2 {
		t.Fatalf("redis order = %+v", result)
	}
}

func TestW11CSuppressionSuppressMetadataBranches(t *testing.T) {
	now := int64(10_000)
	store := newTestSuppressionStore(func() int64 { return now }, nil, false)
	// A longer active suppression is preserved when a shorter one arrives.
	store.SuppressForGatewayFailure("w11c-acc", "w11c-acc", "first", "")
	since := int64(1_234)
	store.Suppress("w11c-acc", 10, "shorter", AvailabilityStatusLocalSuppressed, &suppressionMetadata{sinceMs: &since})
	snapshot := store.SnapshotAvailability(func(string) bool { return false })
	entry, ok := snapshot["w11c-acc"]
	if !ok {
		t.Fatalf("suppression missing after shorter overlay")
	}
	if entry.Since == "" {
		t.Fatalf("since not rendered: %+v", entry)
	}
}

func TestW11CSuppressionHelpers(t *testing.T) {
	// truncateString shortens long values.
	if got := truncateString("abcdef", 3); got != "abc" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncateString("ab", 5); got != "ab" {
		t.Fatalf("short truncate = %q", got)
	}
	// localSuppressionConcurrencyAccountID fallback.
	if got := localSuppressionConcurrencyAccountID(&LocalAccountSuppression{AccountID: "acc"}); got != "acc" {
		t.Fatalf("concurrency fallback = %q", got)
	}
	if got := localSuppressionConcurrencyAccountID(&LocalAccountSuppression{AccountConcurrencyAccountID: "conc", AccountID: "acc"}); got != "conc" {
		t.Fatalf("concurrency override = %q", got)
	}
	// isLocalSuppressionBlocking respects the until timestamp.
	precheck := &LocalAccountSuppression{Status: AvailabilityStatusPrecheckPending, UntilMs: 10_000}
	if !isLocalSuppressionBlocking(precheck, 0, func(string) int { return 0 }) {
		t.Fatalf("precheck pending should block before until")
	}
	if isLocalSuppressionBlocking(precheck, 20_000, func(string) int { return 0 }) {
		t.Fatalf("precheck pending should stop blocking after until")
	}
	// canAcquireLocalHalfOpenLease requires half-open status.
	if canAcquireLocalHalfOpenLease(precheck, 0, func(string) int { return 0 }) {
		t.Fatalf("precheck cannot acquire half-open lease")
	}
}
