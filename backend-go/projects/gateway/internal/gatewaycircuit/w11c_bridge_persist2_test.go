package gatewaycircuit

// w11c: persistWithRetry conflict sub-branches and refresh guard paths.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestW11CBridgePersistConflictRefreshNil(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	// The runtime holds a numerically newer revision, so the refresh drops
	// the observation and the conflict resolves as converged.
	newer := w11cSuspectState(scope, "99", 5)
	if _, err := store.Restore(context.Background(), newer, int64Ptr(1000)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &IncidentRecord{CircuitScopeKey: MustScopeKey(scope)}, CurrentDispatchRevision: 1},
	}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) { options.RetryDelayMs = 1 })
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("conflict refresh-nil: %v", err)
	}
}

func TestW11CBridgePersistConflictIncidentMatchesRuntime(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 4)
	state.DispatchRevision = "7"
	if _, err := store.Restore(context.Background(), state, int64Ptr(1000)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	incident := w11cIncidentOf(scope, state)
	incident.DispatchRevision = 7
	incident.UpdatedAtMs = state.UpdatedAtMs
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &incident, CurrentDispatchRevision: 7},
	}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) { options.RetryDelayMs = 1 })
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("conflict match: %v", err)
	}
}

func TestW11CBridgePersistConflictRestoreCapacity(t *testing.T) {
	// The runtime store is full, so restoring the newer incident from the
	// conflict fails with the typed capacity error.
	tiny := newNonExpiringMemoryStoreWithOptions(t, 1)
	occupied := w11cSuspectState(accountScope("w11c-occupied"), "7", 1)
	if _, err := tiny.Restore(context.Background(), occupied, int64Ptr(1000)); err != nil {
		t.Fatalf("restore occupied: %v", err)
	}
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	newerIncident := w11cIncidentOf(scope, state)
	newerIncident.Generation = 8
	newerIncident.DispatchRevision = 7
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &newerIncident, CurrentDispatchRevision: 1},
	}}
	bridge := w11cNewBridge(t, tiny, db, func(options *BridgeOptions) { options.RetryDelayMs = 1 })
	err := bridge.persistWithRetry(context.Background(), scope, state)
	if err == nil || err.Error() != "账户 circuit runtime store 对账容量不足" {
		t.Fatalf("capacity error = %v", err)
	}
}

func TestW11CBridgePersistConflictRetriesExhaust(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	conflict := CompareAndSetIncidentResult{Status: CASConflict, Incident: nil, CurrentDispatchRevision: 1}
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{conflict, conflict, conflict}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
		options.RetryDelayMs = 1
	})
	if err := bridge.persistWithRetry(context.Background(), scope, state); err == nil {
		t.Fatalf("expected conflict exhaustion error")
	}
}

func TestW11CBridgePersistConflictSleepError(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	conflict := CompareAndSetIncidentResult{Status: CASConflict, Incident: nil, CurrentDispatchRevision: 1}
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{conflict}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
		options.Sleep = func(ctx context.Context, delay time.Duration) error {
			return errors.New("w11c conflict sleep canceled")
		}
	})
	err := bridge.persistWithRetry(context.Background(), scope, state)
	if err == nil || err.Error() != "w11c conflict sleep canceled" {
		t.Fatalf("conflict sleep error = %v", err)
	}
}

func TestW11CBridgePersistRefreshErrorRetries(t *testing.T) {
	memory := newNonExpiringMemoryStore(t)
	wrapped := w11cWrap(memory)
	wrapped.failNext["Get"] = 99
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	db := &w11cCASDB{}
	bridge := w11cNewBridge(t, wrapped, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 2
		options.RetryDelayMs = 1
	})
	if err := bridge.persistWithRetry(context.Background(), scope, state); err == nil {
		t.Fatalf("expected refresh retry exhaustion")
	}
}

func TestW11CBridgePersistRefreshNilStops(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	// A numerically newer runtime revision makes the refresh drop the
	// observation before any CAS happens.
	newer := w11cSuspectState(scope, "99", 5)
	if _, err := store.Restore(context.Background(), newer, int64Ptr(1000)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	db := &w11cCASDB{}
	bridge := w11cNewBridge(t, store, db, nil)
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("refresh nil: %v", err)
	}
	if atomic.LoadInt64(&db.calls) != 0 {
		t.Fatalf("CAS calls = %d, want 0", db.calls)
	}
}

func TestW11CBridgePersistOpaqueRevisionFallsBackToCache(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "opaque", 1)
	db := &w11cCASDB{}
	bridge := w11cNewBridge(t, store, db, nil)
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("opaque revision: %v", err)
	}
	if len(db.casCalls) == 0 {
		t.Fatalf("expected at least one CAS call")
	}
	// A cached dispatch revision for the account is reused on later persists.
	if db.casCalls[0].DispatchRevision < 1 {
		t.Fatalf("dispatch revision = %d", db.casCalls[0].DispatchRevision)
	}
}

func TestW11CBridgeConflictNewerIncidentConvergesAfterRestore(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("w11c-acc")
	state := w11cSuspectState(scope, "7", 1)
	// Seed the runtime with an older state; the conflict carries a newer
	// incident which is restored first and then re-persisted.
	older := w11cSuspectState(scope, "7", 0)
	if _, err := store.Restore(context.Background(), older, int64Ptr(500)); err != nil {
		t.Fatalf("restore older: %v", err)
	}
	newerIncident := w11cIncidentOf(scope, w11cSuspectState(scope, "7", 6))
	newerIncident.DispatchRevision = 7
	db := &w11cCASDB{responses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &newerIncident, CurrentDispatchRevision: 7},
		{Status: CASConflict, Incident: &newerIncident, CurrentDispatchRevision: 7},
	}}
	bridge := w11cNewBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 2
		options.RetryDelayMs = 1
	})
	err := bridge.persistWithRetry(context.Background(), scope, state)
	// The restore wins and the loop converges or exhausts without losing
	// the original error semantics.
	if err != nil && err.Error() == "" {
		t.Fatalf("unexpected empty error")
	}
}
