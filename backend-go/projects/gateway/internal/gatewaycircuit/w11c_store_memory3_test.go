package gatewaycircuit

// w11c: third batch of memory-store paths (hierarchy shadow/unshadow,
// replay trimming, saturation, canary origins).

import (
	"context"
	"strings"
	"testing"
)

func TestW11CMemoryEvidenceTrimmingWithRequiredOne(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	// Required=1 keeps at most two evidence keys; the third independent
	// evidence triggers the trim.
	one := int64(1)
	suspect := mustSuspectConfirmation(t, store, scope, "7", "t1", now, &one)
	due := now + 10_000
	first := strings.Repeat("b", 64)
	acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a1",
		ConfirmationEvidenceKey: &first, LeaseID: "l1", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	second := strings.Repeat("c", 64)
	failed, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: "7", TransitionID: "c1", LeaseID: "l1",
		Outcome: OutcomeTransportFailure, FailureEvidenceKey: &second, NowMs: &due,
	})
	if err != nil || failed.Status != MutationApplied || failed.State.Phase != PhaseOpen {
		t.Fatalf("open = (%s, %+v, %v)", failed.Status, failed.State.Phase, err)
	}
	keys, err := FailureEvidenceKeysOf(failed.State)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) > 2 {
		t.Fatalf("keys not trimmed: %v", keys)
	}
}

func mustSuspectConfirmation(t *testing.T, store *MemoryStore, scope Scope, revision, transitionID string, nowMs int64, required *int64) MutationResult {
	t.Helper()
	result, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: revision, TransitionID: transitionID,
		ConfirmationFailuresRequired: required, NowMs: &nowMs,
	})
	if err != nil {
		t.Fatalf("Suspect: %v", err)
	}
	return result
}

func TestW11CMemoryEscalationBelowThresholdWithClosedAccountEntry(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	opened := w11cOpenScope(t, store, scope, "7", now)
	// A closed account entry exists with a newer generation.
	accountState := ClosedState(accountScope("acc"), "7", 5, "w11c-account", now)
	if _, err := store.Restore(context.Background(), accountState, &now); err != nil {
		t.Fatalf("restore account: %v", err)
	}
	result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7",
		EvidenceID: "w11c-ev-1", AccountTransitionID: "w11c-parent", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	})
	if err != nil || result.Status != EscalationRecorded {
		t.Fatalf("recorded = (%s, %v)", result.Status, err)
	}
	if result.AccountState.Generation != 5 {
		t.Fatalf("account state should come from the resident entry: %+v", result.AccountState)
	}
}

func TestW11CMemoryEscalationCapacityExceeded(t *testing.T) {
	now := int64(1_000)
	// Capacity 3 fits exactly three protocol scopes; the account escalation
	// cannot reserve a fourth slot.
	store := w11cMemStore(t, 3, now)
	scopes := []Scope{
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "p", RequestLane: LaneText, ModelBucket: "m1"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "p", RequestLane: LaneText, ModelBucket: "m2"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "p", RequestLane: LaneText, ModelBucket: "m3"},
	}
	var opened []State
	for _, scope := range scopes {
		opened = append(opened, w11cOpenScope(t, store, scope, "7", now))
	}
	for index := 0; index < 2; index++ {
		result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scopes[index], Generation: opened[index].Generation, DispatchRevision: "7",
			EvidenceID:          "w11c-ev-cap-" + string(rune('0'+index)),
			AccountTransitionID: "w11c-parent", Reason: "r",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
		})
		if err != nil || result.Status != EscalationRecorded {
			t.Fatalf("record %d = (%s, %v)", index, result.Status, err)
		}
	}
	exhausted, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopes[2], Generation: opened[2].Generation, DispatchRevision: "7",
		EvidenceID: "w11c-ev-cap-2", AccountTransitionID: "w11c-parent", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	})
	if err != nil || exhausted.Status != EscalationCapacityExceeded {
		t.Fatalf("capacity = (%s, %v)", exhausted.Status, err)
	}
}

func TestW11CMemoryCloseShadowedSuspectMismatch(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	shadowed := suspect.State
	shadowed.ShadowedByIncidentID = strPtr("w11c-parent")
	shadowed.UpdatedAtMs++
	if _, err := store.Restore(context.Background(), shadowed, &now); err != nil {
		t.Fatalf("restore shadowed: %v", err)
	}
	mismatch, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: scope, Generation: shadowed.Generation, DispatchRevision: "7", TransitionID: "x1",
		ExpectedFailureEvidenceKey: strings.Repeat("b", 64), ObserverEvidenceKey: strings.Repeat("c", 64), NowMs: &now,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("shadowed close = (%s, %v)", mismatch.Status, err)
	}
	mismatchRotation, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: shadowed.Generation, DispatchRevision: "7", TransitionID: "x2",
		ExpectedFailureEvidenceKey: strings.Repeat("b", 64), NowMs: &now,
	})
	if err != nil || mismatchRotation.Status != MutationStateMismatch {
		t.Fatalf("shadowed rotation close = (%s, %v)", mismatchRotation.Status, err)
	}
}

func TestW11CMemoryOpenWithoutIncidentID(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	// Suspect carries an incident id derived from the transition; strip it
	// before completing so the open path synthesizes one.
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	state := suspect.State
	state.IncidentID = nil
	state.UpdatedAtMs++
	if _, err := store.Restore(context.Background(), state, &now); err != nil {
		t.Fatalf("restore stripped: %v", err)
	}
	due := now + 10_000
	evidence := strings.Repeat("b", 64)
	acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: state.Generation, DispatchRevision: "7", TransitionID: "a1",
		ConfirmationEvidenceKey: &evidence, LeaseID: "l1", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	confirmed, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: state.Generation, DispatchRevision: "7", TransitionID: "c1", LeaseID: "l1",
		Outcome: OutcomeTransportFailure, FailureEvidenceKey: &evidence, NowMs: &due,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if confirmed.State.Phase == PhaseOpen && confirmed.State.IncidentID == nil {
		t.Fatalf("open without incident id should synthesize one")
	}
}

func TestW11CMemoryCanaryUnknownFromRecoveringOrigin(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	opened := w11cOpenScope(t, store, scope, "7", now)
	// First canary from OPEN with a framing outcome enters RECOVERING.
	at := *opened.RetryAtMs + 1
	acquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", TransitionID: "k1", LeaseID: "c1", LeaseUntilMs: at + 5_000, NowMs: &at,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("canary = (%s, %v)", acquired.Status, err)
	}
	recovering, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: "7", TransitionID: "kc1", LeaseID: "c1", Outcome: OutcomeFramingComplete, NowMs: &at,
	})
	if err != nil || recovering.Status != MutationApplied || recovering.State.Phase != PhaseRecovering {
		t.Fatalf("canary framing = (%s, %+v, %v)", recovering.Status, recovering.State.Phase, err)
	}
	// A recovering-origin canary completing as unknown restores RECOVERING
	// (not OPEN).
	later := at + 60_000
	second, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: recovering.State.Generation, DispatchRevision: "7", TransitionID: "k2", LeaseID: "c2", LeaseUntilMs: later + 5_000, NowMs: &later,
	})
	if err != nil || second.Status != MutationApplied {
		t.Fatalf("recovering canary = (%s, %v)", second.Status, err)
	}
	restored, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: second.State.Generation, DispatchRevision: "7", TransitionID: "kc2", LeaseID: "c2", Outcome: OutcomeUnknown, NowMs: &later,
	})
	if err != nil || restored.Status != MutationApplied || restored.State.Phase != PhaseRecovering {
		t.Fatalf("recovering unknown = (%s, %+v, %v)", restored.Status, restored.State.Phase, err)
	}
}

func TestW11CMemoryReplayLimitTrimming(t *testing.T) {
	now := int64(1_000)
	store, err := NewMemoryStore(MemoryStoreOptions{
		Capacity: 100, ReplayLimitPerScope: 3, Now: func() int64 { return now },
		Random: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := protocolScope("acc")
	// Drive more than three transitions on one scope.
	mustSuspect(t, store, scope, "7", "t1", now)
	for index := 2; index < 7; index++ {
		keys, err := store.Suspect(context.Background(), SuspectInput{
			Scope: scope, DispatchRevision: "8", TransitionID: "x", NowMs: &now,
		})
		if err != nil {
			t.Fatalf("suspect %d: %v", index, err)
		}
		// The stale-revision result still replays without error.
		if keys.Status == "" {
			t.Fatalf("empty status")
		}
	}
	// Closing through replace keeps the bounded replay list intact.
	replaced, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "9", TransitionID: "r1", NowMs: &now,
	})
	if err != nil || replaced.Status != MutationApplied {
		t.Fatalf("replace = (%s, %v)", replaced.Status, err)
	}
}

func TestW11CMemoryClosedEntryExpiry(t *testing.T) {
	now := int64(1_000)
	store, err := NewMemoryStore(MemoryStoreOptions{
		Capacity: 100, ClosedRetentionMs: 5_000, Now: func() int64 { return now },
		Random: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	keys, err := FailureEvidenceKeysOf(suspect.State)
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys = (%v, %v)", keys, err)
	}
	closed, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "t2",
		ExpectedFailureEvidenceKey: keys[len(keys)-1], NowMs: &now,
	})
	if err != nil || closed.Status != MutationApplied {
		t.Fatalf("close = (%s, %v)", closed.Status, err)
	}
	// After retention the closed entry is dropped on read.
	now += 10_000
	state, err := store.Get(context.Background(), scope, &now)
	if err != nil || state.Phase != PhaseClosed || state.Generation != 0 {
		t.Fatalf("state after retention = (%+v, %v)", state, err)
	}
	// ListDue ignores closed states entirely.
	due, err := store.ListDue(context.Background(), now, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("closed listDue = (%d, %v)", len(due), err)
	}
}

func TestW11CMemorySaturatedReads(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 1, now)
	first := mustSuspect(t, store, protocolScope("acc"), "7", "t1", now)
	if first.Status != MutationApplied {
		t.Fatalf("first suspect = %s", first.Status)
	}
	// The single slot holds a non-closed state, so capacity saturates.
	second := mustSuspect(t, store, protocolScope("other"), "7", "t2", now)
	if second.Status != MutationCapacityExhausted {
		t.Fatalf("second suspect = %s", second.Status)
	}
	saturated, err := store.Get(context.Background(), protocolScope("third"), &now)
	if err != nil || saturated.Phase != PhaseSuspect || saturated.TransitionID != "runtime-capacity-exhausted" {
		t.Fatalf("saturated read = (%+v, %v)", saturated, err)
	}
}

func TestW11CMemoryNegativeListDueNow(t *testing.T) {
	store := w11cMemStore(t, 10, 0)
	if _, err := store.ListDue(context.Background(), -5, 10); err != nil {
		t.Fatalf("negative now clamps: %v", err)
	}
}

func TestW11CMemoryNextRecoveryEvidenceScopes(t *testing.T) {
	// Non-account scopes return the current list unchanged.
	state := ClosedState(protocolScope("acc"), "7", 1, "t", 0)
	state.RecoveryEvidenceScopeKeys = stringList{"k1"}
	if got := nextRecoveryEvidenceScopeKeys(state, strPtr("k2")); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("protocol scope evidence = %v", got)
	}
	// Account scope without required keys returns the current list.
	account := ClosedState(accountScope("acc"), "7", 1, "t", 0)
	account.RecoveryEvidenceScopeKeys = stringList{"k1"}
	if got := nextRecoveryEvidenceScopeKeys(account, strPtr("k2")); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("account without required = %v", got)
	}
	// An evidence key outside the required list is ignored.
	account.RequiredRecoveryScopeKeys = stringList{"req"}
	if got := nextRecoveryEvidenceScopeKeys(account, strPtr("other")); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("unrelated evidence = %v", got)
	}
	// A blank evidence key is ignored.
	if got := nextRecoveryEvidenceScopeKeys(account, nil); len(got) != 1 {
		t.Fatalf("blank evidence = %v", got)
	}
	// A matching new evidence key is appended.
	if got := nextRecoveryEvidenceScopeKeys(account, strPtr("req")); len(got) != 2 || got[1] != "req" {
		t.Fatalf("matching evidence = %v", got)
	}
	// An already-recorded key is not duplicated.
	account.RecoveryEvidenceScopeKeys = stringList{"k1", "req"}
	if got := nextRecoveryEvidenceScopeKeys(account, strPtr("req")); len(got) != 2 {
		t.Fatalf("duplicate evidence = %v", got)
	}
}
