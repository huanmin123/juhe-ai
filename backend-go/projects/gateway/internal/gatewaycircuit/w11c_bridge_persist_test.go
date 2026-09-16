package gatewaycircuit

// w11c: persistWithRetry deep branches, account-load reconcile flow and
// default sleep/timer wiring.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// w11cCASDB replays canned CAS responses in order.
type w11cCASDB struct {
	mockControlPlaneDB
	responses []CompareAndSetIncidentResult
	errs      []error
	calls     int64
}

func (m *w11cCASDB) CompareAndSetIncident(_ context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
	m.mu.Lock()
	m.casCalls = append(m.casCalls, input)
	m.mu.Unlock()
	index := int(atomic.AddInt64(&m.calls, 1)) - 1
	if index < len(m.errs) && m.errs[index] != nil {
		return CompareAndSetIncidentResult{}, m.errs[index]
	}
	if index < len(m.responses) {
		return m.responses[index], nil
	}
	return CompareAndSetIncidentResult{Status: CASApplied, CurrentDispatchRevision: input.DispatchRevision}, nil
}

func w11cSuspectState(scope Scope, revision string, generation int64) State {
	state := ClosedState(scope, revision, generation, "w11c-t", 1000)
	state.Phase = PhaseSuspect
	state.ConfirmationFailuresRequired = int64Ptr(2)
	state.ConfirmationFailureCount = int64Ptr(0)
	state.FailureEvidenceKeys = stringList{}
	state.RetryAtMs = int64Ptr(4000)
	return state
}

func TestW11CBridgePersistTerminalStatuses(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)

	// Account-not-found is terminal without retries.
	notFound := &w11cCASDB{responses: []CompareAndSetIncidentResult{{Status: CASAccountNotFound}}}
	bridge := w11cNewBridge(t, store, notFound, nil)
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("account not found should be terminal: %v", err)
	}
	if atomic.LoadInt64(&notFound.calls) != 1 {
		t.Fatalf("calls = %d, want 1", notFound.calls)
	}

	// Stale dispatch revision is terminal too.
	stale := &w11cCASDB{responses: []CompareAndSetIncidentResult{{Status: CASStaleDispatchRevision, CurrentDispatchRevision: 9}}}
	staleBridge := w11cNewBridge(t, store, stale, nil)
	if err := staleBridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("stale revision should be terminal: %v", err)
	}
	if atomic.LoadInt64(&stale.calls) != 1 {
		t.Fatalf("stale calls = %d, want 1", stale.calls)
	}
}

func TestW11CBridgePersistConflictWithNewerIncident(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)

	// A conflict carrying a newer incident converges through a runtime
	// restore and a successful retry.
	newerIncident := w11cIncidentOf(scope, state)
	newerIncident.Generation = state.Generation + 3
	conflict := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &newerIncident, CurrentDispatchRevision: 1},
	}}
	bridge := w11cNewBridge(t, store, conflict, func(options *BridgeOptions) {
		options.RetryDelayMs = 1
	})
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("conflict convergence: %v", err)
	}
	if atomic.LoadInt64(&conflict.calls) < 2 {
		t.Fatalf("calls = %d, want at least 2", conflict.calls)
	}
}

func TestW11CBridgePersistConflictWithOlderIncident(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	// Seed the runtime with a state newer than the incident the conflict
	// carries, so the observed state is re-persisted.
	newer := w11cSuspectState(scope, "7", 9)
	newer.UpdatedAtMs = 9000
	if _, err := store.Restore(context.Background(), newer, int64Ptr(9000)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state := w11cSuspectState(scope, "7", 1)
	olderIncident := w11cIncidentOf(scope, state)
	conflict := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &olderIncident, CurrentDispatchRevision: 1},
	}}
	bridge := w11cNewBridge(t, store, conflict, func(options *BridgeOptions) {
		options.RetryDelayMs = 1
	})
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("older conflict: %v", err)
	}
}

func TestW11CBridgePersistRetriesExhaust(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	db := &w11cCASDB{errs: []error{
		errors.New("w11c cas 1"), errors.New("w11c cas 2"), errors.New("w11c cas 3"),
	}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
		options.RetryDelayMs = 1
	})
	err := bridge.persistWithRetry(context.Background(), scope, state)
	if err == nil {
		t.Fatalf("expected retry exhaustion error")
	}
}

func TestW11CBridgePersistSleepCancellation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	db := &w11cCASDB{errs: []error{errors.New("w11c cas 1")}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
		options.Sleep = func(ctx context.Context, delay time.Duration) error {
			return errors.New("w11c sleep canceled")
		}
	})
	if err := bridge.persistWithRetry(context.Background(), scope, state); err == nil || err.Error() != "w11c sleep canceled" {
		t.Fatalf("sleep error should propagate: %v", err)
	}
}

func TestW11CBridgePersistBuildInputValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	// An invalid confirmation count fails during input building.
	state := w11cSuspectState(scope, "7", 1)
	bad := int64(-3)
	state.ConfirmationFailureCount = &bad
	bridge := w11cNewBridge(t, store, &w11cCASDB{}, nil)
	if err := bridge.persistWithRetry(context.Background(), scope, state); err == nil {
		t.Fatalf("expected input building error")
	}
}

func TestW11CBridgeObserveDrainsWithDefaultWiring(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// Default sleep/timer: retries use the real 1ms timer.
	bridge, err := NewBridge(BridgeOptions{Store: store, DB: db, RetryDelayMs: 1})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	defer bridge.Close()
	scope := accountScope("w11c-acc-drain")
	bridge.Observe(scope, w11cSuspectState(scope, "7", 1))
	w11cWaitForScopeDrained(t, bridge, MustScopeKey(scope))
	// The persisted incident is recorded in the mock.
	if len(db.casCalls) == 0 {
		t.Fatalf("no CAS call recorded")
	}
}

func TestW11CBridgeEnsureBackoffSuppressesRetries(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	var failures int64
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			return nil, errors.New("w11c incidents failed")
		}
		options.RetryDelayMs = 60_000
		options.OnReadinessFailure = func(ReadinessFailure) { atomic.AddInt64(&failures, 1) }
	})
	// First failure arms the backoff window.
	if ready, err := bridge.EnsureAccountReady(context.Background(), "w11c-acc"); err != nil || ready {
		t.Fatalf("first ensure = (%v, %v)", ready, err)
	}
	bridge.mu.Lock()
	_, armed := bridge.readinessFailures["w11c-acc"]
	bridge.mu.Unlock()
	if !armed {
		t.Fatalf("readiness failure backoff not armed")
	}
	// Within the backoff window Ensure returns not-ready without loading.
	if ready, err := bridge.EnsureAccountReady(context.Background(), "w11c-acc"); err != nil || ready {
		t.Fatalf("backoff ensure = (%v, %v)", ready, err)
	}
	if atomic.LoadInt64(&failures) != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
	// After Close, a waiter parked on a shared in-flight load surfaces an
	// error from the stop channel.
	gate := make(chan struct{})
	blockedStore := newNonExpiringMemoryStore(t)
	blockedDB := &mockControlPlaneDB{}
	blocked := newTestBridge(t, blockedStore, blockedDB, func(options *BridgeOptions) {
		options.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			<-gate
			return nil, nil
		}
	})
	go func() {
		if _, err := blocked.EnsureAccountReady(context.Background(), "w11c-blocked"); err != nil {
			t.Errorf("blocked ensure: %v", err)
		}
	}()
	time.Sleep(30 * time.Millisecond)
	blocked.Close()
	if _, err := blocked.EnsureAccountReady(context.Background(), "w11c-blocked"); err == nil {
		t.Fatalf("expected stopped bridge error")
	}
	close(gate)
}

func TestW11CBridgeRebuildDeferredParentsAndCapacity(t *testing.T) {
	// A page with a parent incident defers its restore until after the
	// children; the capacity guard aborts with the typed reason.
	parentScope := accountScope("w11c-parent")
	parentState := w11cSuspectState(parentScope, "7", 1)
	parentIncident := w11cIncidentOf(parentScope, parentState)
	parentIncident.ChildIncidentIDs = []string{"w11c-child"}

	tiny := newNonExpiringMemoryStoreWithOptions(t, 1)
	occupied := w11cSuspectState(accountScope("w11c-occupied"), "7", 1)
	if _, err := tiny.Restore(context.Background(), occupied, int64Ptr(1000)); err != nil {
		t.Fatalf("restore occupied: %v", err)
	}
	db := &mockControlPlaneDB{rebuildPages: []RebuildPage{{Items: []IncidentRecord{parentIncident}}}}
	bridge := newTestBridge(t, tiny, db, nil)
	result, err := bridge.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonCapacityExhausted {
		t.Fatalf("capacity rebuild = (%+v, %v)", result, err)
	}
}

func TestW11CBridgeRebuildRestoreError(t *testing.T) {
	// An incident carrying an invalid confirmation count fails the restore.
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	incident := w11cIncidentOf(scope, state)
	incident.ConfirmationFailuresRequired = -1
	db := &mockControlPlaneDB{rebuildPages: []RebuildPage{{Items: []IncidentRecord{incident}}}}
	bridge := newTestBridge(t, store, db, nil)
	result, err := bridge.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonRebuildFailed {
		t.Fatalf("restore error rebuild = (%+v, %v)", result, err)
	}
}

func TestW11CBridgeRebuildCursorRegression(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	incident := w11cIncidentOf(scope, state)
	// Page one returns a cursor; page two returns the same cursor again.
	cursor := &RebuildCursor{UpdatedAtMs: 5000, CircuitScopeKey: incident.CircuitScopeKey}
	db := &mockControlPlaneDB{rebuildPages: []RebuildPage{
		{Items: []IncidentRecord{incident}, NextCursor: cursor},
		{Items: nil, NextCursor: cursor},
	}}
	bridge := newTestBridge(t, store, db, nil)
	result, err := bridge.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("cursor regression = (%+v, %v)", result, err)
	}
}

func TestW11CBridgeProjectPendingFailures(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	token := "w11c-token"

	// Claim failure propagates.
	claimFail := &w11cClaimFailDB{err: errors.New("w11c claim failed")}
	bridge := w11cNewBridge(t, store, claimFail, nil)
	if _, err := bridge.ProjectPending(context.Background(), 10); err == nil {
		t.Fatalf("expected claim error")
	}

	// A failing projection is released; release failure propagates.
	releaseFail := &w11cReleaseFailDB{}
	releaseFail.claimed = []OutboxEvent{{
		EventID: "w11c-event", EventType: OutboxEventTypeIncidentChanged, CircuitScopeKey: strPtr(""), ClaimToken: &token,
	}}
	releaseBridge := w11cNewBridge(t, store, releaseFail, nil)
	if _, err := releaseBridge.ProjectPending(context.Background(), 10); err == nil {
		t.Fatalf("expected release error")
	}

	// Ack failure propagates after a successful projection.
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	incident := w11cIncidentOf(scope, state)
	ackFail := &w11cAckFailDB{}
	ackFail.claimed = []OutboxEvent{{
		EventID: "w11c-event-ok", EventType: OutboxEventTypeIncidentChanged,
		CircuitScopeKey: strPtr(incident.CircuitScopeKey), ClaimToken: &token,
	}}
	ackFail.incidents = map[string]IncidentRecord{"w11c-acc": incident}
	ackBridge := w11cNewBridge(t, store, ackFail, nil)
	if _, err := ackBridge.ProjectPending(context.Background(), 10); err == nil {
		t.Fatalf("expected ack error")
	}
}

type w11cClaimFailDB struct {
	mockControlPlaneDB
	err error
}

func (m *w11cClaimFailDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	return nil, m.err
}

type w11cReleaseFailDB struct {
	mockControlPlaneDB
	claimed []OutboxEvent
}

func (m *w11cReleaseFailDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	return m.claimed, nil
}

func (m *w11cReleaseFailDB) ReleaseOutboxForReplay(_ context.Context, _ ReleaseOutboxInput) error {
	return errors.New("w11c release failed")
}

type w11cAckFailDB struct {
	mockControlPlaneDB
	claimed []OutboxEvent
}

func (m *w11cAckFailDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	return m.claimed, nil
}

func (m *w11cAckFailDB) AckOutbox(_ context.Context, _ AckOutboxInput) (AckOutboxResult, error) {
	return AckOutboxResult{}, errors.New("w11c ack failed")
}

func TestW11CBridgeIncidentRelationshipHelpers(t *testing.T) {
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 4)
	incident := w11cIncidentOf(scope, state)
	incident.UpdatedAtMs = state.UpdatedAtMs
	incident.DispatchRevision = 7
	state.DispatchRevision = "7"
	if !incidentMatchesRuntimeState(&incident, state) {
		t.Fatalf("incident should match its runtime state")
	}
	// A newer incident (higher generation) wins.
	newer := incident
	newer.Generation = state.Generation + 1
	if !incidentIsNewerThanRuntimeState(&newer, state) {
		t.Fatalf("newer incident should win")
	}
	if incidentIsNewerThanRuntimeState(&incident, state) {
		t.Fatalf("same-generation incident should not be newer")
	}
	// runtimePhase maps persisting states to open.
	if runtimePhase(IncidentStatePersisting) != PhaseOpen || runtimePhase(PhaseSuspect) != PhaseSuspect {
		t.Fatalf("runtimePhase mapping broken")
	}
	// stateDispatchRevisionNumber falls back for opaque revisions.
	if stateDispatchRevisionNumber(State{DispatchRevision: "opaque"}) != -9223372036854775808 {
		t.Fatalf("opaque revision fallback broken")
	}
}
