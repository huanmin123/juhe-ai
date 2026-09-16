package gatewaycircuit

// w11c: second batch of memory-store paths (suspect over closed entry,
// shadowed mismatch, evidence trimming, escalation capacity, restore errors).

import (
	"context"
	"strings"
	"testing"
)

func TestW11CMemorySuspectAfterClosedEntry(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	first := mustSuspect(t, store, scope, "7", "t1", now)
	if first.State.Generation != 1 {
		t.Fatalf("first generation = %d", first.State.Generation)
	}
	// Close the suspect so a closed entry stays resident, then suspect again.
	keys, err := FailureEvidenceKeysOf(first.State)
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys = (%v, %v)", keys, err)
	}
	closed, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: first.State.Generation, DispatchRevision: "7", TransitionID: "t-close",
		ExpectedFailureEvidenceKey: keys[len(keys)-1], NowMs: &now,
	})
	if err != nil || closed.Status != MutationApplied {
		t.Fatalf("close = (%s, %v)", closed.Status, err)
	}
	second := mustSuspect(t, store, scope, "7", "t2", now)
	if second.Status != MutationApplied || second.State.Generation != 2 {
		t.Fatalf("second suspect = (%s, gen %d)", second.Status, second.State.Generation)
	}
}

func TestW11CMemoryAcquireGuardsShadowedAndDuplicateEvidence(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	due := now + 10_000

	// Acquire with a confirmation evidence key already recorded on the state.
	recordedKeys, err := FailureEvidenceKeysOf(suspect.State)
	if err != nil || len(recordedKeys) == 0 {
		t.Fatalf("keys = (%v, %v)", recordedKeys, err)
	}
	recorded := recordedKeys[len(recordedKeys)-1]
	dup, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a1",
		ConfirmationEvidenceKey: &recorded, LeaseID: "l1", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || dup.Status != MutationStateMismatch {
		t.Fatalf("duplicate evidence = (%s, %v)", dup.Status, err)
	}
	// Successful acquire then shadow the scope; a second acquire mismatches.
	acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a2",
		ConfirmationEvidenceKey: strPtr(strings.Repeat("b", 64)), LeaseID: "l2", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	shadowed := acquired.State
	shadowed.ShadowedByIncidentID = strPtr("w11c-parent")
	shadowed.UpdatedAtMs++
	restored, err := store.Restore(context.Background(), shadowed, &due)
	if err != nil || restored.Status != MutationApplied {
		t.Fatalf("restore shadowed = (%s, %v)", restored.Status, err)
	}
	mismatch, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: "7", TransitionID: "a3",
		ConfirmationEvidenceKey: strPtr(strings.Repeat("c", 64)), LeaseID: "l3", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("shadowed mismatch = (%s, %v)", mismatch.Status, err)
	}
}

func TestW11CMemoryObserverEvidenceDuplicateMismatch(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	keys, err := FailureEvidenceKeysOf(suspect.State)
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys = (%v, %v)", keys, err)
	}
	duplicate := keys[len(keys)-1]
	mismatch, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "x1",
		ExpectedFailureEvidenceKey: duplicate, ObserverEvidenceKey: duplicate, NowMs: &now,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("observer duplicate = (%s, %v)", mismatch.Status, err)
	}
	// A fresh observer key closes the suspect.
	fresh := strings.Repeat("b", 64)
	closed, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "x2",
		ExpectedFailureEvidenceKey: duplicate, ObserverEvidenceKey: fresh, NowMs: &now,
	})
	if err != nil || closed.Status != MutationApplied || closed.State.Phase != PhaseClosed {
		t.Fatalf("observer close = (%s, %+v, %v)", closed.Status, closed.State.Phase, err)
	}
}

func TestW11CMemoryConfirmationEvidenceTrimming(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	// Required=1 keeps at most two evidence keys.
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
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
	if err != nil || failed.Status != MutationApplied {
		t.Fatalf("complete = (%s, %v)", failed.Status, err)
	}
	keys, err := FailureEvidenceKeysOf(failed.State)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) > 2 {
		t.Fatalf("evidence keys not trimmed: %v", keys)
	}
}

func TestW11CMemoryCanaryReplayAndLeaseConflict(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	opened := w11cOpenScope(t, store, scope, "7", now)
	if opened.RetryAtMs == nil {
		t.Fatalf("missing retryAt")
	}
	at := *opened.RetryAtMs + 1
	acquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", TransitionID: "k1", LeaseID: "c1", LeaseUntilMs: at + 5_000, NowMs: &at,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("canary = (%s, %v)", acquired.Status, err)
	}
	// A concurrent canary acquire hits the live lease.
	conflict, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", TransitionID: "k2", LeaseID: "c2", LeaseUntilMs: at + 5_000, NowMs: &at,
	})
	if err != nil || conflict.Status != MutationStateMismatch {
		t.Fatalf("canary conflict = (%s, %v)", conflict.Status, err)
	}
	// Completing with an unknown outcome restores the origin.
	restored, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: "7", TransitionID: "kc1", LeaseID: "c1", Outcome: OutcomeUnknown, NowMs: &at,
	})
	if err != nil || restored.Status != MutationApplied || restored.State.Phase != PhaseOpen {
		t.Fatalf("canary unknown = (%s, %+v, %v)", restored.Status, restored.State.Phase, err)
	}
	// Replay of the same completion transition is idempotent.
	replay, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: "7", TransitionID: "kc1", LeaseID: "c1", Outcome: OutcomeUnknown, NowMs: &at,
	})
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("canary replay = (%s, %v)", replay.Status, err)
	}
	// Replay of the acquire transition is idempotent too.
	acquireReplay, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", TransitionID: "k1", LeaseID: "c1", LeaseUntilMs: at + 5_000, NowMs: &at,
	})
	if err != nil || acquireReplay.Status != MutationIdempotent {
		t.Fatalf("canary acquire replay = (%s, %v)", acquireReplay.Status, err)
	}
}

func TestW11CMemoryEscalationStaleAccountRevisionAndCapacity(t *testing.T) {
	now := int64(1_000)
	// Capacity 8 leaves room for 3 protocol scopes + 1 account scope only.
	store := w11cMemStore(t, 8, now)
	scopeA := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-a"}
	scopeB := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-b"}
	scopeC := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-c"}
	scopeD := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-d"}
	openA := w11cOpenScope(t, store, scopeA, "7", now)
	openB := w11cOpenScope(t, store, scopeB, "7", now)
	openC := w11cOpenScope(t, store, scopeC, "7", now)
	openD := w11cOpenScope(t, store, scopeD, "7", now)

	// Record evidence from the first three scopes (below threshold=4).
	for index, opened := range []State{openA, openB, openC} {
		scopes := []Scope{scopeA, scopeB, scopeC}
		result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scopes[index], Generation: opened.Generation, DispatchRevision: "7",
			EvidenceID:          "w11c-ev-" + string(rune('a'+index)),
			AccountTransitionID: "w11c-parent", Reason: "r",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 4, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
		})
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
		if result.Status != EscalationRecorded {
			t.Fatalf("record %d = %s", index, result.Status)
		}
	}
	// The fourth distinct scope crosses the threshold and escalates.
	escalated, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopeD, Generation: openD.Generation, DispatchRevision: "7", EvidenceID: "w11c-ev-escalate", AccountTransitionID: "w11c-parent", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 4, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	})
	if err != nil || escalated.Status != EscalationEscalated {
		t.Fatalf("escalated = (%s, %v)", escalated.Status, err)
	}
	// An already-open account entry with the matching revision attaches.
	scopeE := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-e"}
	openE := w11cOpenScope(t, store, scopeE, "7", now)
	active, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopeE, Generation: openE.Generation, DispatchRevision: "7", EvidenceID: "w11c-ev-active", AccountTransitionID: "w11c-parent", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 4, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	})
	if err != nil || active.Status != EscalationAlreadyActive {
		t.Fatalf("already active = (%s, %v)", active.Status, err)
	}
	// A stale account revision blocks further escalation.
	accountState := ClosedState(accountScope("acc"), "9", escalated.AccountState.Generation+1, "w11c-account", now+1)
	accountState.Phase = PhaseOpen
	if _, err := store.Restore(context.Background(), accountState, int64Ptr(now+1)); err != nil {
		t.Fatalf("restore account: %v", err)
	}
	scopeF := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-f"}
	openF := w11cOpenScope(t, store, scopeF, "7", now+1)
	stale, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopeF, Generation: openF.Generation, DispatchRevision: "7", EvidenceID: "w11c-ev-stale", AccountTransitionID: "w11c-parent", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 4, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: int64Ptr(now + 1),
	})
	if err != nil || stale.Status != EscalationStaleRevision {
		t.Fatalf("stale escalation = (%s, %v)", stale.Status, err)
	}
}

func TestW11CMemoryEscalationEvidenceWindowTrimming(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	// maxProtocolScopes=3 trims the oldest evidence entries.
	for index := 0; index < 4; index++ {
		bucket := string(rune('w')) + strings.Repeat(string(rune('0'+index)), 2)
		scope := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: bucket}
		opened := w11cOpenScope(t, store, scope, "7", now)
		result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scope, Generation: opened.Generation, DispatchRevision: "7",
			EvidenceID:          "w11c-ev-" + bucket,
			AccountTransitionID: "w11c-parent", Reason: "r",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 3, NowMs: &now,
		})
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
		if result.ProtocolScopeCount > 3 {
			t.Fatalf("scope count not trimmed: %d", result.ProtocolScopeCount)
		}
	}
}

func TestW11CMemoryRestoreValidationAndCapacity(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 1, now)
	scope := protocolScope("acc")
	// A state with invalid confirmation failures fails normalization.
	bad := int64(-5)
	state := mustSuspect(t, store, scope, "7", "t1", now).State
	state.ConfirmationFailuresRequired = &bad
	if _, err := store.Restore(context.Background(), state, &now); err == nil {
		t.Fatalf("expected normalization error")
	}
	// Capacity exhaustion on a fresh scope.
	other := protocolScope("other")
	otherState := ClosedState(other, "3", 1, "w11c-x", now)
	otherState.Phase = PhaseOpen
	otherState.UpdatedAtMs = now + 1
	capacity, err := store.Restore(context.Background(), otherState, int64Ptr(now+1))
	if err != nil || capacity.Status != MutationCapacityExhausted {
		t.Fatalf("capacity restore = (%s, %v)", capacity.Status, err)
	}
}

func TestW11CMemoryReplaceAccountDispatchRevisionEvidenceCleanup(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	w11cRecordEvidenceForScopes(t, store, "acc", "7", now, 2, "w11c-ev-r")
	// Replacing the account revision clears the stale escalation evidence and
	// closes every matching family scope.
	scope := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-model-0"}
	changed, err := store.ReplaceAccountDispatchRevision(context.Background(), ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "acc", DispatchRevision: "9", TransitionID: "w11c-radr", NowMs: &now,
	})
	if err != nil {
		t.Fatalf("replace account revision: %v", err)
	}
	if changed < 1 {
		t.Fatalf("changed = %d, want at least 1", changed)
	}
	state, err := store.Get(context.Background(), scope, &now)
	if err != nil || state.Phase != PhaseClosed || state.DispatchRevision != "9" {
		t.Fatalf("state after replace = (%+v, %v)", state, err)
	}
}
