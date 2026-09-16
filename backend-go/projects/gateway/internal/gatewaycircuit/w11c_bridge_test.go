package gatewaycircuit

// w11c: bridge constructor defaults, readiness gates, rebuild failures,
// reconcile guards and outbox projection branches.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestW11CBridgeConstructorDefaults(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// Minimal options: every injectable default kicks in.
	bridge, err := NewBridge(BridgeOptions{Store: store, DB: db})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	defer bridge.Close()
	if bridge.sleep == nil || bridge.newTimer == nil || bridge.persistIncident == nil ||
		bridge.loadRebuildPage == nil || bridge.loadAccountIncidents == nil || bridge.onReadinessFailure == nil {
		t.Fatalf("defaults not wired: %+v", bridge)
	}
	if bridge.ownerID == "" || bridge.retryDelayMs <= 0 || bridge.maxPersistAttempts <= 0 {
		t.Fatalf("owner/retry defaults missing: %s %d %d", bridge.ownerID, bridge.retryDelayMs, bridge.maxPersistAttempts)
	}
	// Validation failures.
	for _, tc := range []struct {
		name string
		opts BridgeOptions
	}{
		{"store", BridgeOptions{DB: db}},
		{"db", BridgeOptions{Store: store}},
		{"retryDelay", BridgeOptions{Store: store, DB: db, RetryDelayMs: -1}},
		{"maxPersistAttempts", BridgeOptions{Store: store, DB: db, MaxPersistAttempts: -2}},
		{"closedRetention", BridgeOptions{Store: store, DB: db, ClosedRetentionMs: -3}},
		{"rebuildPageSize", BridgeOptions{Store: store, DB: db, RebuildPageSize: -4}},
		{"rebuildMaxPages", BridgeOptions{Store: store, DB: db, RebuildMaxPages: -5}},
		{"rebuildPageTimeout", BridgeOptions{Store: store, DB: db, RebuildPageTimeoutMs: -6}},
		{"rebuildTotalTimeout", BridgeOptions{Store: store, DB: db, RebuildTotalTimeoutMs: -7}},
	} {
		if _, err := NewBridge(tc.opts); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
	// An explicit owner id is preserved.
	owned, err := NewBridge(BridgeOptions{Store: store, DB: db, OwnerID: " w11c-owner "})
	if err != nil {
		t.Fatalf("NewBridge owner: %v", err)
	}
	defer owned.Close()
	if owned.ownerID != "w11c-owner" {
		t.Fatalf("owner = %q", owned.ownerID)
	}
}

func TestW11CBridgeReadinessGuards(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	// Empty runtime key errors.
	if _, err := bridge.IsAccountReady(" "); err == nil {
		t.Fatalf("expected runtime key validation error")
	}
	if _, err := bridge.EnsureAccountReady(context.Background(), ""); err == nil {
		t.Fatalf("expected ensure runtime key validation error")
	}
	// Not globally ready yet.
	ready, err := bridge.IsAccountReady("w11c-acc")
	if err != nil || ready {
		t.Fatalf("initial readiness = (%v, %v)", ready, err)
	}

	// Observe after close is a no-op.
	bridge.Close()
	scope := protocolScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "t1", 0)
	state.Phase = PhaseSuspect
	bridge.Observe(scope, state)
}

func TestW11CBridgeEnsureAccountReadyWaitsAndErrors(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	gate := make(chan struct{})
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			<-gate
			return nil, nil
		}
		options.RetryDelayMs = 60_000
	})
	defer func() { close(gate) }()
	// The first ensure blocks inside the account load.
	go func() {
		ready, err := bridge.EnsureAccountReady(context.Background(), "w11c-acc")
		if err != nil || !ready {
			t.Errorf("blocked ensure = (%v, %v)", ready, err)
		}
	}()
	// Let the first ensure reach the in-flight load.
	time.Sleep(30 * time.Millisecond)
	// A second ensure with a cancelled context surfaces ctx.Err() while
	// waiting on the shared load.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bridge.EnsureAccountReady(ctx, "w11c-acc"); err == nil {
		t.Fatalf("expected context cancellation error")
	}
}

func TestW11CBridgeRebuildSingleFlightAndFailures(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	pageGate := make(chan struct{})
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.LoadRebuildPage = func(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
			select {
			case <-pageGate:
				return RebuildPage{}, errors.New("w11c page load failed")
			case <-ctx.Done():
				return RebuildPage{}, ctx.Err()
			}
		}
		options.RebuildTotalTimeoutMs = 60_000
	})
	// Two concurrent rebuilds share one in-flight call.
	var wg sync.WaitGroup
	results := make([]RebuildResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for index := range results {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = bridge.Rebuild(context.Background())
		}(index)
	}
	// Let the in-flight call reach the gated page load first.
	time.Sleep(50 * time.Millisecond)
	// A cancelled waiter exits with its own error while the call runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bridge.Rebuild(ctx); err == nil {
		t.Fatalf("expected cancelled rebuild error")
	}
	close(pageGate)
	wg.Wait()
	for index := range results {
		if errs[index] != nil || !results[index].Blocked {
			t.Fatalf("rebuild %d = (%+v, %v)", index, results[index], errs[index])
		}
	}
	// Failure keeps the gate closed.
	if bridge.IsReady() {
		t.Fatalf("bridge should stay not ready after failed rebuild")
	}
}

func TestW11CBridgeRebuildMaxPagesAndTimeout(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// Endless pages exceed the max-page bound.
	cycling := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.RebuildMaxPages = 2
		options.LoadRebuildPage = func(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
			return RebuildPage{NextCursor: &RebuildCursor{UpdatedAtMs: input.NowMs, CircuitScopeKey: "w11c-next"}}, nil
		}
	})
	result, err := cycling.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("max pages = (%+v, %v)", result, err)
	}

	// A monotonic clock past the total budget times out once it advances.
	ticks := int64(0)
	overBudget := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.RebuildTotalTimeoutMs = 1
		options.MonotonicNow = func() time.Duration {
			ticks++
			if ticks > 1 {
				return time.Hour
			}
			return 0
		}
		options.LoadRebuildPage = func(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
			return RebuildPage{}, nil
		}
	})
	result, err = overBudget.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonRebuildTimeout {
		t.Fatalf("timeout = (%+v, %v)", result, err)
	}
}

func TestW11CBridgeReconcileActiveGuards(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	// Not ready gate returns zero regardless of the limit.
	repaired, err := bridge.ReconcileActive(context.Background(), 0)
	if err != nil || repaired != 0 {
		t.Fatalf("reconcile before ready = (%d, %v)", repaired, err)
	}

	// Ready gate with a page error propagates.
	ready := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.LoadRebuildPage = func(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
			return RebuildPage{}, errors.New("w11c reconcile page failed")
		}
	})
	ready.mu.Lock()
	ready.globallyReady = true
	ready.mu.Unlock()
	if _, err := ready.ReconcileActive(context.Background(), 10); err == nil {
		t.Fatalf("expected reconcile page error")
	}
	// Limit validation happens once the gate is open.
	if _, err := ready.ReconcileActive(context.Background(), 0); err == nil {
		t.Fatalf("expected limit validation error")
	}
}

func TestW11CBridgeReconcileActiveRepairsAndBlocks(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	scope := accountScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	state.Phase = PhaseSuspect
	incident := w11cIncidentOf(scope, state)
	db.incidents = map[string]IncidentRecord{
		"w11c-acc": incident,
	}
	db.rebuildPages = []RebuildPage{{Items: []IncidentRecord{incident}}}
	bridge := newTestBridge(t, store, db, nil)
	bridge.mu.Lock()
	bridge.globallyReady = true
	bridge.mu.Unlock()
	repaired, err := bridge.ReconcileActive(context.Background(), 10)
	if err != nil || repaired != 1 {
		t.Fatalf("reconcile = (%d, %v)", repaired, err)
	}

	// Capacity exhaustion during reconcile surfaces a typed error.
	tiny := newNonExpiringMemoryStoreWithOptions(t, 1)
	occupied := ClosedState(accountScope("w11c-occupied"), "7", 1, "w11c-t0", 1000)
	occupied.Phase = PhaseSuspect
	if _, err := tiny.Restore(context.Background(), occupied, int64Ptr(1000)); err != nil {
		t.Fatalf("restore occupied: %v", err)
	}
	tinyDB := &mockControlPlaneDB{}
	tinyIncident := w11cIncidentOf(scope, state)
	tinyDB.incidents = map[string]IncidentRecord{
		"w11c-acc2": tinyIncident,
	}
	tinyDB.rebuildPages = []RebuildPage{{Items: []IncidentRecord{tinyIncident}}}
	tinyBridge := newTestBridge(t, tiny, tinyDB, nil)
	tinyBridge.mu.Lock()
	tinyBridge.globallyReady = true
	tinyBridge.mu.Unlock()
	if _, err := tinyBridge.ReconcileActive(context.Background(), 10); err == nil {
		t.Fatalf("expected reconcile capacity error")
	}
}

func newNonExpiringMemoryStoreWithOptions(t *testing.T, capacity int64) *MemoryStore {
	t.Helper()
	if capacity < 1 {
		capacity = 1
	}
	store, err := NewMemoryStore(MemoryStoreOptions{
		Capacity: capacity, Now: func() int64 { return 0 },
		Random:   func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

func w11cIncidentOf(scope Scope, state State) IncidentRecord {
	scopeKey := MustScopeKey(scope)
	incidentID := state.TransitionID
	if state.IncidentID != nil && *state.IncidentID != "" {
		incidentID = *state.IncidentID
	}
	return IncidentRecord{
		AccountID:         RuntimeAccountIDFromKey(scope.AccountRuntimeKey),
		AccountRuntimeKey: scope.AccountRuntimeKey,
		CircuitScopeKey:   scopeKey,
		ScopeKind:         IncidentScopeKindAccount,
		IncidentID:        incidentID,
		State:             state.Phase,
		Generation:        state.Generation,
		DispatchRevision:  1,
		LedgerRevision:    1,
		TransitionID:      state.TransitionID,
		ConfirmationFailuresRequired:    2,
		ConfirmationFailureEvidenceKeys: []string{},
	}
}

// w11cClaimingDB extends the recorded mock with canned outbox claims.
type w11cClaimingDB struct {
	mockControlPlaneDB
	claimed []OutboxEvent
}

func (m *w11cClaimingDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	return m.claimed, nil
}

func w11cNewBridge(t *testing.T, store Store, db ControlPlaneDB, mutate func(*BridgeOptions)) *Bridge {
	t.Helper()
	options := BridgeOptions{
		Store: store,
		DB:    db,
		Now:   func() int64 { return 1000 },
		Sleep: func(context.Context, time.Duration) error { return nil },
		NewTimer: func(delay time.Duration) (<-chan struct{}, func()) {
			done := make(chan struct{})
			return done, func() {}
		},
	}
	if mutate != nil {
		mutate(&options)
	}
	bridge, err := NewBridge(options)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)
	return bridge
}

func TestW11CBridgeProjectPendingGuards(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	// Invalid limit errors.
	if _, err := bridge.ProjectPending(context.Background(), 0); err == nil {
		t.Fatalf("expected limit validation error")
	}

	// A dispatch-revision event projects into the store.
	token := "w11c-token"
	db2 := &w11cClaimingDB{}
	db2.claimed = []OutboxEvent{{
		EventID: "w11c-event-1", EventType: OutboxEventTypeDispatchRevisionChanged,
		AccountRuntimeKey: "w11c-acc", DispatchRevision: 9, TransitionID: "w11c-t", ClaimToken: &token,
	}}
	projecting := w11cNewBridge(t, store, db2, nil)
	acknowledged, err := projecting.ProjectPending(context.Background(), 10)
	if err != nil || acknowledged != 1 {
		t.Fatalf("project dispatch revision = (%d, %v)", acknowledged, err)
	}

	// An incident event with a missing scope key is released for replay.
	db3 := &w11cClaimingDB{}
	db3.claimed = []OutboxEvent{{
		EventID: "w11c-event-2", EventType: OutboxEventTypeIncidentChanged, ClaimToken: &token,
	}}
	releasing := w11cNewBridge(t, store, db3, nil)
	acknowledged, err = releasing.ProjectPending(context.Background(), 10)
	if err != nil || acknowledged != 0 {
		t.Fatalf("missing scope key = (%d, %v)", acknowledged, err)
	}
	if len(db3.release) != 1 {
		t.Fatalf("release calls = %d", len(db3.release))
	}

	// An incident event whose ledger record is missing is released.
	db4 := &w11cClaimingDB{}
	db4.claimed = []OutboxEvent{{
		EventID: "w11c-event-3", EventType: OutboxEventTypeIncidentChanged, CircuitScopeKey: strPtr("w11c-missing"), ClaimToken: &token,
	}}
	missing := w11cNewBridge(t, store, db4, nil)
	if acknowledged, err := missing.ProjectPending(context.Background(), 10); err != nil || acknowledged != 0 {
		t.Fatalf("missing incident = (%d, %v)", acknowledged, err)
	}

	// An incident with unresolved children loads the account incidents.
	db5 := &w11cClaimingDB{}
	scope := accountScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	state.Phase = PhaseSuspect
	incident := w11cIncidentOf(scope, state)
	incident.ChildIncidentIDs = []string{"w11c-child"}
	db5.incidents = map[string]IncidentRecord{"w11c-acc": incident}
	db5.claimed = []OutboxEvent{{
		EventID: "w11c-event-4", EventType: OutboxEventTypeIncidentChanged, CircuitScopeKey: strPtr(incident.CircuitScopeKey), ClaimToken: &token,
	}}
	unresolved := w11cNewBridge(t, store, db5, nil)
	if acknowledged, err := unresolved.ProjectPending(context.Background(), 10); err != nil || acknowledged != 1 {
		t.Fatalf("unresolved children = (%d, %v)", acknowledged, err)
	}
}

func TestW11CBridgeAccountLoadFailureReasons(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "account_load_failed"},
		{&rebuildError{reason: RebuildReasonRebuildTimeout}, "account_load_timeout"},
		{errors.New("account_load_runtime_key_mismatch"), "account_load_runtime_key_mismatch"},
		{errors.New("account_load_capacity_exhausted"), "account_load_capacity_exhausted"},
		{errors.New("account_load_persistence_failure"), "account_load_persistence_failure"},
		{errors.New("other"), "account_load_failed"},
	}
	for _, tc := range cases {
		if got := accountLoadFailureReason(tc.err); got != tc.want {
			t.Fatalf("accountLoadFailureReason(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestW11CBridgeEnsureAccountReadyMismatchAndCapacity(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	scope := accountScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	state.Phase = PhaseSuspect
	mismatched := w11cIncidentOf(scope, state)
	mismatched.AccountRuntimeKey = "w11c-other"
	db.incidents = map[string]IncidentRecord{"w11c-acc": mismatched}
	bridge := newTestBridge(t, store, db, nil)
	ready, err := bridge.EnsureAccountReady(context.Background(), "w11c-acc")
	if err != nil || ready {
		t.Fatalf("mismatched load = (%v, %v)", ready, err)
	}

	// Capacity exhausted during the account load.
	tiny := newNonExpiringMemoryStoreWithOptions(t, 1)
	// Occupy the single slot so the restore cannot reserve capacity.
	occupied := ClosedState(accountScope("w11c-occupied"), "7", 1, "w11c-t0", 1000)
	occupied.Phase = PhaseSuspect
	if _, err := tiny.Restore(context.Background(), occupied, int64Ptr(1000)); err != nil {
		t.Fatalf("restore occupied: %v", err)
	}
	tinyDB := &mockControlPlaneDB{}
	tinyDB.incidents = map[string]IncidentRecord{"w11c-acc2": w11cIncidentOf(scope, state)}
	tinyBridge := newTestBridge(t, tiny, tinyDB, nil)
	ready, err = tinyBridge.EnsureAccountReady(context.Background(), "w11c-acc2")
	if err != nil || ready {
		t.Fatalf("capacity load = (%v, %v)", ready, err)
	}
}

func TestW11CBridgeLoadAccountIncidentsErrorNotified(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	failures := make(chan ReadinessFailure, 4)
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			return nil, errors.New("w11c incidents failed")
		}
		options.OnReadinessFailure = func(failure ReadinessFailure) {
			failures <- failure
		}
	})
	ready, err := bridge.EnsureAccountReady(context.Background(), "w11c-acc")
	if err != nil || ready {
		t.Fatalf("ensure = (%v, %v)", ready, err)
	}
	select {
	case failure := <-failures:
		if failure.Operation != "account_load" || failure.AccountRuntimeKey != "w11c-acc" || failure.Reason != "account_load_failed" {
			t.Fatalf("failure = %+v", failure)
		}
	case <-time.After(time.Second):
		t.Fatalf("readiness failure not notified")
	}
}

func TestW11CBridgePersistWithRetryBranches(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	state.Phase = PhaseSuspect

	// A scope whose runtime key has no valid account id fails fast.
	badScope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: ":authorized:x"}
	if err := w11cPersistDirect(t, store, badScope, state); err == nil {
		t.Fatalf("expected account id validation error")
	}

	// Persist failures exhaust the retry budget and report the raw error.
	db := &mockControlPlaneDB{}
	db.casErrs = []error{errors.New("w11c cas failed"), errors.New("w11c cas failed"), errors.New("w11c cas failed")}
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
		options.RetryDelayMs = 1
	})
	bridge.Observe(scope, state)
	w11cWaitForPersistenceFailure(t, bridge, MustScopeKey(scope))
}

func w11cPersistDirect(t *testing.T, store Store, scope Scope, state State) error {
	t.Helper()
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)
	return bridge.persistWithRetry(context.Background(), scope, state)
}

func w11cWaitForPersistenceFailure(t *testing.T, bridge *Bridge, scopeKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		_, failed := bridge.persistenceFailures[scopeKey]
		bridge.mu.Unlock()
		if failed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("persistence failure not recorded for %s", scopeKey)
}

func TestW11CBridgeRefreshDesiredStatePrecedence(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)
	ctx := context.Background()
	scope := accountScope("w11c-acc")
	observed := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	observed.Phase = PhaseSuspect

	// A newer runtime generation wins over the observed state.
	newer := observed
	newer.Generation = 5
	newer.UpdatedAtMs = 2000
	newer.TransitionID = "w11c-t2"
	if _, err := store.Restore(ctx, newer, int64Ptr(2000)); err != nil {
		t.Fatalf("restore newer: %v", err)
	}
	refreshed, err := bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed == nil || refreshed.Generation != 5 {
		t.Fatalf("newer runtime = (%+v, %v)", refreshed, err)
	}
	// Same generation with a newer timestamp wins.
	same := observed
	same.Generation = 5
	same.UpdatedAtMs = 3000
	same.TransitionID = "w11c-t3"
	if _, err := store.Restore(ctx, same, int64Ptr(3000)); err != nil {
		t.Fatalf("restore same: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed == nil || refreshed.UpdatedAtMs != 3000 {
		t.Fatalf("newer timestamp = (%+v, %v)", refreshed, err)
	}
	// A numerically newer runtime revision drops the observation.
	revised := same
	revised.DispatchRevision = "99"
	revised.Generation = 6
	revised.UpdatedAtMs = 4000
	revised.TransitionID = "w11c-t4"
	if _, err := store.Restore(ctx, revised, int64Ptr(4000)); err != nil {
		t.Fatalf("restore revised: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed != nil {
		t.Fatalf("newer revision = (%+v, %v)", refreshed, err)
	}
	// Missing runtime state keeps the observation.
	fresh, err := bridge.refreshDesiredState(ctx, accountScope("w11c-missing"), observed)
	if err != nil || fresh == nil || fresh.Generation != observed.Generation {
		t.Fatalf("missing runtime = (%+v, %v)", fresh, err)
	}
}

func TestW11CBridgePersistConflictConvergence(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.RetryDelayMs = 1
	})
	scope := accountScope("w11c-acc")
	state := ClosedState(scope, "7", 1, "w11c-t1", 1000)
	state.Phase = PhaseSuspect
	// First CAS reports a conflict carrying the newer incident; the runtime
	// state is newer so the observed state wins on the retry.
	newerIncident := w11cIncidentOf(scope, state)
	newerIncident.Generation = 9
	db.casResponses = []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &newerIncident, CurrentDispatchRevision: 9},
	}
	bridge.Observe(scope, state)
	// The retry path persists the observed state eventually.
	w11cWaitForScopeDrained(t, bridge, MustScopeKey(scope))
}

func w11cWaitForScopeDrained(t *testing.T, bridge *Bridge, scopeKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		_, pending := bridge.pending[scopeKey]
		_, failed := bridge.persistenceFailures[scopeKey]
		bridge.mu.Unlock()
		if !pending && !failed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scope %s never drained", scopeKey)
}

func TestW11CBridgeHelpers(t *testing.T) {
	// msToDuration / durationToMs round trip.
	if got := durationToMs(msToDuration(1500)); got != 1500 {
		t.Fatalf("round trip = %d", got)
	}
	// compareCursor ordering.
	older := RebuildCursor{UpdatedAtMs: 1, CircuitScopeKey: "a"}
	newer := RebuildCursor{UpdatedAtMs: 2, CircuitScopeKey: "a"}
	if compareCursor(&newer, &older) <= 0 {
		t.Fatalf("newer cursor should compare greater")
	}
	// requiredIncidentPart / requiredIncidentRequestLane guards.
	if got := requiredIncidentPart(strPtr(" x "), "part"); got != "x" {
		t.Fatalf("part = %q", got)
	}
	w11cExpectPanic(t, func() { requiredIncidentPart(nil, "part") })
	w11cExpectPanic(t, func() { requiredIncidentPart(strPtr(" "), "part") })
	if got := requiredIncidentRequestLane(strPtr(LaneImage)); got != LaneImage {
		t.Fatalf("lane = %q", got)
	}
	w11cExpectPanic(t, func() { requiredIncidentRequestLane(nil) })
	w11cExpectPanic(t, func() { requiredIncidentRequestLane(strPtr("bogus")) })
	// msToDuration maps milliseconds to time.Duration.
	if msToDuration(5) != 5*time.Millisecond {
		t.Fatalf("msToDuration misbehaves")
	}
	// sameStringSet comparison.
	if !sameStringSet([]string{"a", "b"}, []string{"b", "a"}) || sameStringSet([]string{"a"}, []string{"b"}) {
		t.Fatalf("sameStringSet misbehaves")
	}
	if strings.TrimSpace("") != "" {
		t.Fatalf("sanity")
	}
}

func w11cExpectPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	fn()
}
