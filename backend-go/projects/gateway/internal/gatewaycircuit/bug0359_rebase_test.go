package gatewaycircuit

// BUG-0359 回归：Redis 运行态条目过期后新状态从 generation 1 重计，而控制面
// 仍持有更高 generation 的 CLOSED 墓碑。Bridge 必须在 cas_conflict 时按墓碑
// generation +1 rebase，并在 CAS applied 后把新 generation 写回运行态；旧观察
// 与新 dispatch 世代不得被 rebase。

import (
	"context"
	"testing"
)

func bug0359Suspect(scope Scope, revision string, generation int64, updatedAtMs int64) State {
	state := ClosedState(scope, revision, generation, "t-bug0359", updatedAtMs)
	state.Phase = PhaseSuspect
	state.FailureReason = strPtr("transport_failure")
	return state
}

func bug0359Incident(scope Scope, generation, dispatchRevision, ledgerRevision, updatedAtMs int64) IncidentRecord {
	return IncidentRecord{
		CircuitScopeKey:                 MustScopeKey(scope),
		AccountID:                       RuntimeAccountIDFromKey(scope.AccountRuntimeKey),
		AccountRuntimeKey:               scope.AccountRuntimeKey,
		ScopeKind:                       IncidentScopeKindAccount,
		IncidentID:                      "incident-bug0359",
		ChildIncidentIDs:                []string{},
		State:                           PhaseClosed,
		Generation:                      generation,
		DispatchRevision:                dispatchRevision,
		LedgerRevision:                  ledgerRevision,
		TransitionID:                    "t-closed",
		ConfirmationFailuresRequired:    1,
		ConfirmationFailureEvidenceKeys: []string{},
		UpdatedAtMs:                     updatedAtMs,
	}
}

func TestBug0359RebaseDesiredStateOnIncident(t *testing.T) {
	scope := accountScope("bug0359")
	desired := bug0359Suspect(scope, "13", 1, 2_000)

	t.Run("expired runtime generation is rebased above the tombstone", func(t *testing.T) {
		rebased, ok := rebaseDesiredStateOnIncident(desired, bug0359Incident(scope, 20, 12, 41, 1_000))
		if !ok {
			t.Fatal("expected rebase")
		}
		if rebased.Generation != 21 || rebased.DispatchRevision != "13" {
			t.Fatalf("rebased = gen %d dr %s, want gen 21 dr 13", rebased.Generation, rebased.DispatchRevision)
		}
		if desired.Generation != 1 {
			t.Fatalf("input state must stay unchanged, got gen %d", desired.Generation)
		}
	})

	t.Run("newer dispatch epoch wins over the observation", func(t *testing.T) {
		if _, ok := rebaseDesiredStateOnIncident(desired, bug0359Incident(scope, 20, 14, 41, 1_000)); ok {
			t.Fatal("an incident from a newer dispatch epoch must not be rebased")
		}
	})

	t.Run("stale observation is not resurrected", func(t *testing.T) {
		if _, ok := rebaseDesiredStateOnIncident(desired, bug0359Incident(scope, 20, 12, 41, 2_000)); ok {
			t.Fatal("an observation not newer than the incident must not be rebased")
		}
	})

	t.Run("unknown incident timestamp is not rebased", func(t *testing.T) {
		if _, ok := rebaseDesiredStateOnIncident(desired, bug0359Incident(scope, 20, 12, 41, 0)); ok {
			t.Fatal("zero incident timestamp must not drive a rebase")
		}
	})

	t.Run("higher observation generation is not lowered", func(t *testing.T) {
		newer := desired
		newer.Generation = 30
		if _, ok := rebaseDesiredStateOnIncident(newer, bug0359Incident(scope, 20, 12, 41, 1_000)); ok {
			t.Fatal("an observation above the tombstone must not be rebased")
		}
	})

	t.Run("scope mismatch is rejected", func(t *testing.T) {
		other := bug0359Incident(accountScope("other"), 20, 12, 41, 1_000)
		if _, ok := rebaseDesiredStateOnIncident(desired, other); ok {
			t.Fatal("scope mismatch must not be rebased")
		}
	})

	t.Run("unparseable observation revision is rejected", func(t *testing.T) {
		unknown := desired
		unknown.DispatchRevision = ""
		if _, ok := rebaseDesiredStateOnIncident(unknown, bug0359Incident(scope, 20, 12, 41, 1_000)); ok {
			t.Fatal("an observation without a parseable dispatch revision must not be rebased")
		}
	})
}

func TestBug0359PersistRebasesExpiredRuntimeGeneration(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := accountScope("bug0359")
	state := bug0359Suspect(scope, "13", 1, 2_000)
	if _, err := store.Restore(context.Background(), state, int64Ptr(2_000)); err != nil {
		t.Fatalf("restore runtime state: %v", err)
	}
	conflictIncident := bug0359Incident(scope, 20, 12, 41, 1_000)
	applied := conflictIncident
	applied.Generation = 21
	applied.DispatchRevision = 13
	applied.LedgerRevision = 42
	applied.TransitionID = state.TransitionID
	applied.UpdatedAtMs = state.UpdatedAtMs
	db := &mockControlPlaneDB{casResponses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &conflictIncident, CurrentDispatchRevision: 13},
		{Status: CASApplied, Incident: &applied, CurrentDispatchRevision: 13},
	}}
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) { options.RetryDelayMs = 1 })
	if err := bridge.persistWithRetry(context.Background(), scope, state); err != nil {
		t.Fatalf("persistWithRetry: %v", err)
	}
	db.mu.Lock()
	calls := append([]CompareAndSetIncidentInput(nil), db.casCalls...)
	db.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("CAS calls = %d, want 2", len(calls))
	}
	if calls[1].Generation != 21 {
		t.Fatalf("retried generation = %d, want 21", calls[1].Generation)
	}
	if calls[1].ExpectedLedgerRevision == nil || *calls[1].ExpectedLedgerRevision != 41 {
		t.Fatalf("retried expected ledger = %+v, want 41", calls[1].ExpectedLedgerRevision)
	}
	runtimeState, err := store.Get(context.Background(), scope, int64Ptr(3_000))
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if runtimeState.Generation != 21 {
		t.Fatalf("runtime generation = %d, want 21 after rebase restore", runtimeState.Generation)
	}
}

func TestBug0359PersistRebaseRestoreCapacityIsReported(t *testing.T) {
	// rebase CAS 已 applied 但运行态写回容量不足时不得静默成功：pending 若被
	// 消费，运行态会停在旧代，后续转换继续撞栅栏（与既有对账路径同口径）。
	tiny := newNonExpiringMemoryStoreWithOptions(t, 1)
	occupied := bug0359Suspect(accountScope("bug0359-occupied"), "13", 1, 2_000)
	if _, err := tiny.Restore(context.Background(), occupied, int64Ptr(2_000)); err != nil {
		t.Fatalf("restore occupied: %v", err)
	}
	scope := accountScope("bug0359-capacity")
	state := bug0359Suspect(scope, "13", 1, 2_000)
	conflictIncident := bug0359Incident(scope, 20, 12, 41, 1_000)
	applied := conflictIncident
	applied.Generation = 21
	applied.DispatchRevision = 13
	applied.LedgerRevision = 42
	applied.TransitionID = state.TransitionID
	applied.UpdatedAtMs = state.UpdatedAtMs
	db := &mockControlPlaneDB{casResponses: []CompareAndSetIncidentResult{
		{Status: CASConflict, Incident: &conflictIncident, CurrentDispatchRevision: 13},
		{Status: CASApplied, Incident: &applied, CurrentDispatchRevision: 13},
	}}
	bridge := newTestBridge(t, tiny, db, func(options *BridgeOptions) { options.RetryDelayMs = 1 })
	err := bridge.persistWithRetry(context.Background(), scope, state)
	if err == nil || err.Error() != "账户 circuit runtime store 对账容量不足" {
		t.Fatalf("capacity error = %v", err)
	}
}
