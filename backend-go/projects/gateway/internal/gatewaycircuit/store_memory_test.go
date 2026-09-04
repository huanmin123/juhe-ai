package gatewaycircuit

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func newTestMemoryStore(t *testing.T, capacity int64, now func() int64) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: capacity, Now: now, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

func accountScope(key string) Scope { return Scope{Kind: ScopeKindAccount, AccountRuntimeKey: key} }

func protocolScope(key string) Scope {
	return Scope{
		Kind: ScopeKindProtocolModel, AccountRuntimeKey: key, ProtocolProfile: "profile",
		RequestLane: LaneText, ModelBucket: "gpt-4o",
	}
}

func mustSuspect(t *testing.T, store *MemoryStore, scope Scope, revision, transitionID string, nowMs int64) MutationResult {
	t.Helper()
	result, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: revision, TransitionID: transitionID,
		Reason: "transport:connect failed", NowMs: int64Ptr(nowMs),
	})
	if err != nil {
		t.Fatalf("Suspect: %v", err)
	}
	return result
}

func TestMemorySuspectStateShape(t *testing.T) {
	now := int64(10_000)
	store := newTestMemoryStore(t, 10, func() int64 { return now })
	result := mustSuspect(t, store, accountScope("acc"), "7", "t1", now)
	if result.Status != MutationApplied {
		t.Fatalf("status = %s", result.Status)
	}
	state := result.State
	if state.Phase != PhaseSuspect || state.Generation != 1 {
		t.Fatalf("unexpected phase/generation: %+v", state)
	}
	if state.IncidentID == nil || *state.IncidentID != "t1" {
		t.Fatalf("incidentId = %v", state.IncidentID)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs != now+3000 {
		t.Fatalf("retryAtMs = %v", state.RetryAtMs)
	}
	if state.ConfirmationFailuresRequired == nil || *state.ConfirmationFailuresRequired != DefaultConfirmationFailuresRequired {
		t.Fatalf("confirmationFailuresRequired = %v", state.ConfirmationFailuresRequired)
	}
	if state.ConfirmationFailureCount == nil || *state.ConfirmationFailureCount != 0 {
		t.Fatalf("confirmationFailureCount = %v", state.ConfirmationFailureCount)
	}
	if len(state.FailureEvidenceKeys) != 1 {
		t.Fatalf("failureEvidenceKeys = %v", state.FailureEvidenceKeys)
	}
	// Replay of the same transition is idempotent.
	replay := mustSuspect(t, store, accountScope("acc"), "7", "t1", now)
	if replay.Status != MutationIdempotent {
		t.Fatalf("replay status = %s", replay.Status)
	}
	// A different transition in SUSPECT is a state mismatch.
	mismatch := mustSuspect(t, store, accountScope("acc"), "7", "t2", now)
	if mismatch.Status != MutationStateMismatch {
		t.Fatalf("mismatch status = %s", mismatch.Status)
	}
	// A different revision is stale.
	fresh := newTestMemoryStore(t, 10, func() int64 { return now })
	stale := mustSuspect(t, fresh, accountScope("acc"), "8", "t1", now)
	if stale.Status != MutationApplied {
		t.Fatalf("setup status = %s", stale.Status)
	}
	stale2, err := fresh.Suspect(context.Background(), SuspectInput{
		Scope: accountScope("acc"), DispatchRevision: "9", TransitionID: "t3", Reason: "r", NowMs: int64Ptr(now),
	})
	if err != nil {
		t.Fatalf("Suspect: %v", err)
	}
	if stale2.Status != MutationStaleDispatchRevision {
		t.Fatalf("stale status = %s", stale2.Status)
	}
}

func TestMemoryConfirmationFlowRequiredTwo(t *testing.T) {
	now := int64(100_000)
	clock := now
	store := newTestMemoryStore(t, 10, func() int64 { return clock })
	scope := protocolScope("acc")
	mustSuspect(t, store, scope, "7", "s1", now)

	lease, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "lease-1", LeaseUntilMs: now + 30_000, NowMs: int64Ptr(now),
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.Status != MutationNotDue {
		t.Fatalf("expected not_due before retryAt, got %s", lease.Status)
	}
	clock = now + 3000
	lease, err = store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "lease-1", LeaseUntilMs: clock + 30_000, NowMs: int64Ptr(clock),
	})
	if err != nil || lease.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", lease.Status, err)
	}
	if lease.State.Lease == nil || lease.State.Lease.Kind != LeaseKindConfirmation || lease.State.Lease.LeaseID != "lease-1" {
		t.Fatalf("lease = %+v", lease.State.Lease)
	}

	sha1 := strings.Repeat("1", 64)
	sha2 := strings.Repeat("2", 64)

	// First independent failure keeps SUSPECT.
	first, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-1", Outcome: OutcomeTransportFailure,
		Reason: strPtr("transport:e1"), FailureEvidenceKey: strPtr(sha1), NowMs: int64Ptr(clock),
	})
	if err != nil || first.Status != MutationApplied {
		t.Fatalf("first = (%s, %v)", first.Status, err)
	}
	if first.State.Phase != PhaseSuspect || first.State.ConfirmationFailureCount == nil || *first.State.ConfirmationFailureCount != 1 {
		t.Fatalf("first state = %+v", first.State)
	}
	// Repeating the same evidence must not advance the count.
	repeat, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a2",
		LeaseID: "lease-2", LeaseUntilMs: clock + 60_000, NowMs: int64Ptr(clock + 3000),
	})
	if err != nil || repeat.Status != MutationApplied {
		t.Fatalf("re-acquire = (%s, %v)", repeat.Status, err)
	}
	sameEvidence, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c2",
		LeaseID: "lease-2", Outcome: OutcomeTransportFailure,
		FailureEvidenceKey: strPtr(sha1), NowMs: int64Ptr(clock + 3000),
	})
	if err != nil || sameEvidence.State.Phase != PhaseSuspect || *sameEvidence.State.ConfirmationFailureCount != 1 {
		t.Fatalf("same evidence state = %+v (%v)", sameEvidence.State, err)
	}
	// Third lease with different evidence reaches the threshold and opens.
	third, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a3",
		LeaseID: "lease-3", LeaseUntilMs: clock + 90_000, NowMs: int64Ptr(clock + 6000),
	})
	if err != nil || third.Status != MutationApplied {
		t.Fatalf("third acquire = (%s, %v)", third.Status, err)
	}
	opened, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c3",
		LeaseID: "lease-3", Outcome: OutcomeTransportFailure,
		Reason: strPtr("transport:e2"), FailureEvidenceKey: strPtr(sha2), NowMs: int64Ptr(clock + 6000),
	})
	if err != nil || opened.Status != MutationApplied {
		t.Fatalf("opened = (%s, %v)", opened.Status, err)
	}
	if opened.State.Phase != PhaseOpen {
		t.Fatalf("phase = %s", opened.State.Phase)
	}
	// The incident id from the original suspect is retained on open.
	if opened.State.BackoffAttempt != 1 || opened.State.IncidentID == nil || *opened.State.IncidentID != "s1" {
		t.Fatalf("open state = %+v", opened.State)
	}
	if opened.State.RetryAtMs == nil || *opened.State.RetryAtMs != clock+6000+3000 {
		t.Fatalf("backoff retryAtMs = %v", opened.State.RetryAtMs)
	}
	// A wrong lease id no longer matches.
	mismatch, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c4",
		LeaseID: "lease-3", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha2), NowMs: int64Ptr(clock + 6000),
	})
	if err != nil {
		t.Fatalf("mismatch: %v", err)
	}
	if mismatch.Status != MutationStateMismatch && mismatch.Status != MutationLeaseMismatch {
		t.Fatalf("mismatch status = %s", mismatch.Status)
	}
}

func TestMemoryLeaseExpiryNormalization(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newTestMemoryStore(t, 10, func() int64 { return *clock })
	scope := accountScope("acc")
	mustSuspect(t, store, scope, "7", "s1", 0)
	*clock = 3000
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "l1", LeaseUntilMs: 60_000, NowMs: int64Ptr(3000),
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	*clock = 61_000
	state, err := store.Get(context.Background(), scope, int64Ptr(*clock))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Lease != nil {
		t.Fatalf("expired confirmation lease should be cleared")
	}
	if state.RetryAtMs == nil || *state.RetryAtMs != 61_000 {
		t.Fatalf("retryAtMs = %v", state.RetryAtMs)
	}

	// Drive the circuit to OPEN through two independent confirmed failures,
	// then verify the half-open lease expiry restores OPEN.
	sha := strings.Repeat("1", 64)
	sha2 := strings.Repeat("2", 64)
	*clock = 61_000
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a2",
		LeaseID: "l2", LeaseUntilMs: 90_000, NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if _, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "l2", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha), NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("complete 1: %v", err)
	}
	*clock = 64_000
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a3",
		LeaseID: "l3", LeaseUntilMs: 120_000, NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("acquire 3: %v", err)
	}
	if _, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c2",
		LeaseID: "l3", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha2), NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("complete 2: %v", err)
	}
	if _, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k1",
		LeaseID: "h1", LeaseUntilMs: 120_000, NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("canary acquire: %v", err)
	}
	*clock = 130_000
	state, err = store.Get(context.Background(), scope, int64Ptr(*clock))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Phase != PhaseOpen || state.Lease != nil || state.HalfOpenOrigin != nil {
		t.Fatalf("state after half-open lease expiry = %+v", state)
	}
}

func TestMemoryCanaryRecoveryCycle(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newTestMemoryStore(t, 10, func() int64 { return *clock })
	scope := accountScope("acc")
	// required = 1 so a single confirmed failure opens the circuit.
	required := int64(1)
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1", Reason: "transport:connect failed",
		ConfirmationFailuresRequired: &required, NowMs: int64Ptr(0),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "l1", LeaseUntilMs: 30_000, NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	sha := strings.Repeat("1", 64)
	opened, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "l1", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha),
		NowMs: int64Ptr(*clock),
	})
	if err != nil {
		t.Fatalf("complete confirmation: %v", err)
	}
	if opened.State.Phase != PhaseOpen {
		t.Fatalf("expected OPEN with required=1, got %+v", opened.State)
	}

	// Half-open from OPEN.
	*clock = 60_000
	canary, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k1",
		LeaseID: "h1", LeaseUntilMs: 120_000, NowMs: int64Ptr(*clock),
	})
	if err != nil || canary.Status != MutationApplied {
		t.Fatalf("canary = (%s, %v)", canary.Status, err)
	}
	if canary.State.Lease.Kind != LeaseKindHalfOpen || *canary.State.HalfOpenOrigin != PhaseOpen {
		t.Fatalf("canary state = %+v", canary.State)
	}
	// Framing complete from OPEN origin enters RECOVERING.
	recovered, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k1c",
		LeaseID: "h1", Outcome: OutcomeFramingComplete, NowMs: int64Ptr(*clock),
	})
	if err != nil || recovered.State.Phase != PhaseRecovering {
		t.Fatalf("recovered = (%s, %+v, %v)", recovered.Status, recovered.State, err)
	}
	if recovered.State.BackoffAttempt != 1 || recovered.State.RetryAtMs == nil || *recovered.State.RetryAtMs != *clock+3000 {
		t.Fatalf("recovering state = %+v", recovered.State)
	}
	// unknown restores the origin (OPEN) with the next backoff attempt.
	*clock = 63_000
	if _, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k2",
		LeaseID: "h2", LeaseUntilMs: 200_000, NowMs: int64Ptr(*clock),
	}); err != nil {
		t.Fatalf("canary 2 acquire: %v", err)
	}
	restored, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k2c",
		LeaseID: "h2", Outcome: OutcomeUnknown, NowMs: int64Ptr(*clock),
	})
	// unknown restores halfOpenOrigin (RECOVERING here) with the next
	// backoff attempt.
	if err != nil || restored.State.Phase != PhaseRecovering || restored.State.BackoffAttempt != 2 {
		t.Fatalf("unknown canary = (%s, %+v, %v)", restored.Status, restored.State, err)
	}
	if restored.State.RetryAtMs == nil || *restored.State.RetryAtMs != *clock+5000 {
		t.Fatalf("unknown canary retryAt must use backoff[1], got %v", restored.State.RetryAtMs)
	}

	// Successes accumulate; the third closes (threshold 3).
	*clock = 120_000
	for i := 0; i < 3; i++ {
		leaseID := "r" + string(rune('a'+i))
		acquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
			Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: leaseID + "-t",
			LeaseID: leaseID, LeaseUntilMs: 300_000, NowMs: int64Ptr(*clock),
		})
		if err != nil || acquired.Status != MutationApplied {
			t.Fatalf("recovery canary %d = (%s, %v)", i, acquired.Status, err)
		}
		if acquired.State.Lease.Kind != LeaseKindRecovery {
			t.Fatalf("expected recovery lease, got %s", acquired.State.Lease.Kind)
		}
		completed, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
			Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: leaseID + "-c",
			LeaseID: leaseID, Outcome: OutcomeFramingComplete, NowMs: int64Ptr(*clock),
		})
		if err != nil {
			t.Fatalf("complete canary %d: %v", i, err)
		}
		if i < 2 {
			if completed.State.Phase != PhaseRecovering || completed.State.RecoverySuccessCount != int64(i+1) {
				t.Fatalf("cycle %d state = %+v", i, completed.State)
			}
			*clock += 3000
			continue
		}
		if completed.State.Phase != PhaseClosed {
			t.Fatalf("expected CLOSED after threshold, got %+v", completed.State)
		}
		if completed.State.IncidentID == nil || *completed.State.IncidentID != "s1" {
			t.Fatalf("incident id retained = %v", completed.State.IncidentID)
		}
	}
	// A transport failure in HALF_OPEN reopens with the next backoff attempt.
	// A closed circuit rejects further canaries with stale_generation.
	*clock = 300_000
	stale, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: 2, DispatchRevision: "7", TransitionID: "k3",
		LeaseID: "h3", LeaseUntilMs: 400_000, NowMs: int64Ptr(*clock),
	})
	if err != nil {
		t.Fatalf("stale acquire returned an error: %v", err)
	}
	if stale.Status != MutationStaleGeneration {
		t.Fatalf("stale acquire status = %s", stale.Status)
	}
}

func TransitionIDFor(i int) string  { return string(rune('a' + i)) + "-tid" }
func CompleteIDFor(i int) string    { return string(rune('a' + i)) + "-done" }

func TestMemoryCapacityExhaustionAndClosedEviction(t *testing.T) {
	now := int64(0)
	store := newTestMemoryStore(t, 1, func() int64 { return now })
	first := mustSuspect(t, store, accountScope("a"), "7", "t1", now)
	if first.Status != MutationApplied {
		t.Fatalf("first = %s", first.Status)
	}
	second := mustSuspect(t, store, accountScope("b"), "7", "t2", now)
	if second.Status != MutationCapacityExhausted {
		t.Fatalf("second = %s", second.Status)
	}
	if second.State.FailureReason == nil || *second.State.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("capacity state = %+v", second.State)
	}
	// Get for an unknown scope under saturation returns the capacity state.
	got, err := store.Get(context.Background(), accountScope("c"), int64Ptr(now))
	if err != nil || got.Phase != PhaseSuspect || got.FailureReason == nil || *got.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("saturated get = (%+v, %v)", got, err)
	}
	// Close the first scope via replaceDispatchRevision; closed entries are
	// evictable so the next suspect fits.
	replaced, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: accountScope("a"), DispatchRevision: "8", TransitionID: "r1", NowMs: int64Ptr(now),
	})
	if err != nil || replaced.Status != MutationApplied {
		t.Fatalf("replace = (%s, %v)", replaced.Status, err)
	}
	third := mustSuspect(t, store, accountScope("b"), "7", "t3", now)
	if third.Status != MutationApplied {
		t.Fatalf("third = %s", third.Status)
	}
}

func TestMemoryClosedRetentionExpiry(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newTestMemoryStore(t, 10, func() int64 { return *clock })
	if _, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: accountScope("a"), DispatchRevision: "8", TransitionID: "r1", NowMs: int64Ptr(0),
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if size, _ := store.Size(context.Background()); size != 1 {
		t.Fatalf("size = %d", size)
	}
	*clock = 5*60_000 + 1
	if size, _ := store.Size(context.Background()); size != 0 {
		t.Fatalf("size after retention = %d", size)
	}
}

func TestMemoryRestoreAndParentProjection(t *testing.T) {
	now := int64(50_000)
	store := newTestMemoryStore(t, 50, func() int64 { return now })
	parent := ClosedState(accountScope("acc"), "7", 3, "p1", now)
	parent.Phase = PhaseOpen
	parent.Generation = 3
	parent.TransitionID = "p1"
	parent.IncidentID = strPtr("p1")
	parent.ChildScopeKeys = stringList{MustScopeKey(protocolScope("acc"))}
	parent.ChildIncidentIDs = stringList{"c1"}
	parent.RequiredRecoveryScopeKeys = stringList{MustScopeKey(protocolScope("acc"))}

	child := ClosedState(protocolScope("acc"), "7", 2, "c1t", now)
	child.Phase = PhaseSuspect
	child.Generation = 2
	child.TransitionID = "c1t"
	child.IncidentID = strPtr("c1")
	child.UpdatedAtMs = now - 1000

	if _, err := store.Restore(context.Background(), child, int64Ptr(now)); err != nil {
		t.Fatalf("restore child: %v", err)
	}
	restored, err := store.Restore(context.Background(), parent, int64Ptr(now))
	if err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	if restored.Status != MutationApplied || len(restored.RelatedStates) != 1 {
		t.Fatalf("restore = (%s, %d related)", restored.Status, len(restored.RelatedStates))
	}
	related := restored.RelatedStates[0]
	if related.ScopeKey != MustScopeKey(protocolScope("acc")) || related.ShadowedByIncidentID == nil || *related.ShadowedByIncidentID != "p1" {
		t.Fatalf("related child = %+v", related)
	}
	if !strings.HasPrefix(related.TransitionID, "hierarchy:shadow:") {
		t.Fatalf("hierarchy transitionId = %s", related.TransitionID)
	}
	// Restoring an older child revision again stays idempotent.
	if again, err := store.Restore(context.Background(), child, int64Ptr(now)); err != nil || again.Status != MutationIdempotent {
		t.Fatalf("re-restore = (%s, %v)", again.Status, err)
	}
}

func TestMemoryEscalationToAccountScope(t *testing.T) {
	now := int64(1000)
	clock := &now
	store := newTestMemoryStore(t, 50, func() int64 { return *clock })
	scopes := []Scope{protocolScope("acc"), func() Scope {
		s := protocolScope("acc")
		s.ModelBucket = "gpt-4o-mini"
		return s
	}(), func() Scope {
		s := protocolScope("acc")
		s.RequestLane = LaneImage
		return s
	}()}
	for index, scope := range scopes {
		state := ClosedState(scope, "7", 1, TransitionIDFor(index), *clock)
		state.Phase = PhaseOpen
		state.Generation = 1
		state.TransitionID = TransitionIDFor(index)
		state.IncidentID = strPtr(TransitionIDFor(index))
		if _, err := store.Restore(context.Background(), state, clock); err != nil {
			t.Fatalf("restore %d: %v", index, err)
		}
	}
	// Two distinct scopes stay below the threshold of three.
	for index, scope := range scopes[:2] {
		result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scope, Generation: 1, DispatchRevision: "7",
			EvidenceID: TransitionIDFor(index), AccountTransitionID: "parent-t1",
			Reason: "transport:e", ConfirmedFailureCount: 1,
			DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
		})
		if err != nil || result.Status != EscalationRecorded {
			t.Fatalf("record %d = (%s, %v)", index, result.Status, err)
		}
	}
	// Duplicate evidence is idempotent.
	dup, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopes[0], Generation: 1, DispatchRevision: "7",
		EvidenceID: TransitionIDFor(0), AccountTransitionID: "parent-t1",
		Reason: "transport:e", ConfirmedFailureCount: 1,
		DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
	})
	if err != nil || dup.Status != EscalationIdempotent {
		t.Fatalf("duplicate = (%s, %v)", dup.Status, err)
	}
	// Third scope escalates and shadows every child.
	escalated, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scopes[2], Generation: 1, DispatchRevision: "7",
		EvidenceID: TransitionIDFor(2), AccountTransitionID: "parent-t1",
		Reason: "transport:e", ConfirmedFailureCount: 1,
		DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
	})
	if err != nil || escalated.Status != EscalationEscalated {
		t.Fatalf("escalated = (%s, %v)", escalated.Status, err)
	}
	accountState := escalated.AccountState
	if accountState.Phase != PhaseOpen || accountState.Generation != 1 || accountState.BackoffAttempt != 1 {
		t.Fatalf("account state = %+v", accountState)
	}
	if len(accountState.ChildScopeKeys) != 3 || len(accountState.RequiredRecoveryScopeKeys) != 3 {
		t.Fatalf("account children = %+v", accountState)
	}
	// shadow_children covers every evidence scope, including the current child.
	if len(escalated.RelatedStates) != 3 {
		t.Fatalf("shadowed children = %d", len(escalated.RelatedStates))
	}
	for _, related := range escalated.RelatedStates {
		if related.ShadowedByIncidentID == nil || *related.ShadowedByIncidentID != "parent-t1" {
			t.Fatalf("related = %+v", related)
		}
	}
	// Next evidence while the account incident is active reports already_active.
	extra := ClosedState(protocolScope("acc"), "7", 1, "x", *clock)
	extra.Phase = PhaseOpen
	extra.Generation = 1
	extra.TransitionID = "x"
	extra.IncidentID = strPtr("x")
	if _, err := store.Restore(context.Background(), extra, clock); err != nil {
		t.Fatalf("restore extra: %v", err)
	}
	active, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: extra.Scope, Generation: 1, DispatchRevision: "7",
		EvidenceID: "extra-evidence", AccountTransitionID: "parent-t2",
		Reason: "transport:e", ConfirmedFailureCount: 1,
		DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
	})
	if err != nil || active.Status != EscalationAlreadyActive {
		t.Fatalf("already active = (%s, %v)", active.Status, err)
	}
	// The extra record reuses an existing child scope with an unchanged
	// incident id, so the account relationship transition id is retained
	// (Node attachAccountShadow only rewrites on relationship changes).
	if active.AccountState.TransitionID != "parent-t1" {
		t.Fatalf("account relationship transitionId = %s", active.AccountState.TransitionID)
	}
	// Clearing the evidence and closing the account incident unshadow children.
	cleared, err := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "7", EvidenceID: "e", NowMs: clock,
	})
	if err != nil || !cleared {
		t.Fatalf("clear = (%v, %v)", cleared, err)
	}
	if cleared2, _ := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "other", EvidenceID: "e", NowMs: clock,
	}); cleared2 {
		t.Fatalf("clear with wrong revision must fail")
	}
}

func TestMemoryEscalationRecoveryEvidenceAndCloseUnshadow(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newTestMemoryStore(t, 50, func() int64 { return *clock })
	scopes := []Scope{protocolScope("acc")}
	mini := protocolScope("acc")
	mini.ModelBucket = "gpt-4o-mini"
	scopes = append(scopes, mini)
	image := protocolScope("acc")
	image.RequestLane = LaneImage
	scopes = append(scopes, image)

	for index, scope := range scopes {
		transitionID := string(rune('a'+index)) + "-incident"
		child := ClosedState(scope, "7", 1, transitionID, 4000)
		child.Phase = PhaseOpen
		child.Generation = 1
		child.IncidentID = strPtr(transitionID)
		if _, err := store.Restore(context.Background(), child, clock); err != nil {
			t.Fatalf("restore child %d: %v", index, err)
		}
	}

	// Record three distinct scopes; the third escalates to the account.
	var escalated EscalationResult
	var err error
	for index, scope := range scopes {
		escalated, err = store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scope, Generation: 1, DispatchRevision: "7",
			EvidenceID: string(rune('a'+index)) + "-evidence", AccountTransitionID: "parent-1",
			Reason: "transport:e", ConfirmedFailureCount: 1,
			DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
		})
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
	}
	if escalated.Status != EscalationEscalated {
		t.Fatalf("escalate = %s", escalated.Status)
	}
	accountState := escalated.AccountState
	if len(accountState.RequiredRecoveryScopeKeys) != 3 {
		t.Fatalf("required recovery scope keys = %v", accountState.RequiredRecoveryScopeKeys)
	}

	// The first canary enters RECOVERING (OPEN origin resets the success
	// count); three further successful canaries close the account. Each
	// supplies one of the required child scope keys.
	for i := 0; i < 4; i++ {
		*clock += 3000
		acquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
			Scope: accountScope("acc"), Generation: accountState.Generation,
			DispatchRevision: "7", TransitionID: string(rune('a'+i)) + "-canary",
			LeaseID: "lease" + string(rune('a'+i)), LeaseUntilMs: *clock + 100_000, NowMs: clock,
		})
		if err != nil || acquired.Status != MutationApplied {
			t.Fatalf("canary %d = (%s, %v)", i, acquired.Status, err)
		}
		completed, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
			Scope: accountScope("acc"), Generation: accountState.Generation,
			DispatchRevision: "7", TransitionID: string(rune('a'+i)) + "-done",
			LeaseID: "lease" + string(rune('a'+i)), Outcome: OutcomeFramingComplete,
			EvidenceScopeKey: strPtr(MustScopeKey(scopes[i%3])), NowMs: clock,
		})
		if err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
		if i < 3 {
			if completed.State.Phase != PhaseRecovering ||
				completed.State.RecoverySuccessCount != int64(i) ||
				len(completed.State.RecoveryEvidenceScopeKeys) != i {
				t.Fatalf("cycle %d = %+v", i, completed.State)
			}
			continue
		}
		if completed.State.Phase != PhaseClosed {
			t.Fatalf("account should close, got %+v", completed.State)
		}
	}

	// The closing unshadows every child.
	for _, scope := range scopes {
		state, err := store.Get(context.Background(), scope, clock)
		if err != nil {
			t.Fatalf("get child: %v", err)
		}
		if state.ShadowedByIncidentID != nil {
			t.Fatalf("child %s still shadowed by %s", state.ScopeKey, *state.ShadowedByIncidentID)
		}
		if !strings.HasPrefix(state.TransitionID, "hierarchy:unshadow:") {
			t.Fatalf("child transitionId = %s", state.TransitionID)
		}
	}
}

func TestMemoryReplaceAccountDispatchRevisionClosesFamily(t *testing.T) {
	now := int64(0)
	store := newTestMemoryStore(t, 50, func() int64 { return now })
	base := accountScope("acc")
	authorized := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "acc:authorized:sys:grp:auth"}
	other := accountScope("other")
	for _, scope := range []Scope{base, authorized, other} {
		if result := mustSuspect(t, store, scope, "7", "t-"+scope.AccountRuntimeKey, now); result.Status != MutationApplied {
			t.Fatalf("suspect %s = %s", scope.AccountRuntimeKey, result.Status)
		}
	}
	changed, err := store.ReplaceAccountDispatchRevision(context.Background(), ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "acc", DispatchRevision: "9", TransitionID: "rev9", NowMs: int64Ptr(now),
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2 (base + authorized family)", changed)
	}
	for _, scope := range []Scope{base, authorized} {
		state, err := store.Get(context.Background(), scope, int64Ptr(now))
		if err != nil {
			t.Fatalf("get %s: %v", scope.AccountRuntimeKey, err)
		}
		if state.Phase != PhaseClosed || state.DispatchRevision != "9" || state.Generation != 2 {
			t.Fatalf("%s state = %+v", scope.AccountRuntimeKey, state)
		}
	}
	otherState, _ := store.Get(context.Background(), other, int64Ptr(now))
	if otherState.Phase != PhaseSuspect {
		t.Fatalf("other scope must stay SUSPECT: %+v", otherState)
	}
}

func TestMemoryListDueOrdering(t *testing.T) {
	now := int64(0)
	store := newTestMemoryStore(t, 50, func() int64 { return now })
	mustSuspect(t, store, accountScope("late"), "7", "t-late", now)
	mustSuspect(t, store, accountScope("early"), "7", "t-early", now)
	// early has the same retryAt; ordering ties are insertion-stable. Push the
	// late one further out by finishing one confirmation cycle is unnecessary:
	// both are due at now+3000, limit 1 returns the first inserted (late).
	due, err := store.ListDue(context.Background(), now+3000, 1)
	if err != nil {
		t.Fatalf("listDue: %v", err)
	}
	if len(due) != 1 || due[0].Scope.AccountRuntimeKey != "late" {
		t.Fatalf("due = %+v", due)
	}
	due, err = store.ListDue(context.Background(), now+2999, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due before retry window = (%d, %v)", len(due), err)
	}
}

func TestMemoryConcurrentSuspectExactlyOneApplied(t *testing.T) {
	now := int64(0)
	store := newTestMemoryStore(t, 100, func() int64 { return now })
	const goroutines = 16
	var wg sync.WaitGroup
	statuses := make([]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := store.Suspect(context.Background(), SuspectInput{
				Scope: accountScope("acc"), DispatchRevision: "7",
				TransitionID: TransitionIDFor(index), Reason: "r", NowMs: int64Ptr(now),
			})
			if err != nil {
				statuses[index] = "error:" + err.Error()
				return
			}
			statuses[index] = result.Status
		}(i)
	}
	wg.Wait()
	applied := 0
	for _, status := range statuses {
		switch status {
		case MutationApplied:
			applied++
		case MutationStateMismatch, MutationIdempotent:
		default:
			t.Fatalf("unexpected status %s", status)
		}
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want exactly 1", applied)
	}
}

func TestMemorySuspectValidationErrors(t *testing.T) {
	now := int64(0)
	store := newTestMemoryStore(t, 10, func() int64 { return now })
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: accountScope("a"), DispatchRevision: "  ", TransitionID: "t", Reason: "r", NowMs: int64Ptr(now),
	}); err == nil || err.Error() != "账户电路操作缺少 dispatchRevision" {
		t.Fatalf("empty revision error = %v", err)
	}
	tooBig := int64(9)
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: accountScope("a"), DispatchRevision: "7", TransitionID: "t", Reason: "r",
		ConfirmationFailuresRequired: &tooBig, NowMs: int64Ptr(now),
	}); err == nil || !strings.Contains(err.Error(), "confirmationFailuresRequired 必须是 1..5") {
		t.Fatalf("confirmation failures error = %v", err)
	}
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: accountScope("a"), DispatchRevision: "7", TransitionID: "tp", Reason: "r", NowMs: int64Ptr(now),
	}); err != nil {
		t.Fatalf("setup suspect: %v", err)
	}
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: accountScope("a"), Generation: 1, DispatchRevision: "7", TransitionID: "t",
		LeaseID: "l", LeaseUntilMs: 0, NowMs: int64Ptr(now + 3000),
	}); err == nil || err.Error() != "账户电路租约截止时间必须晚于当前时间" {
		t.Fatalf("lease in the past error = %v", err)
	}
}
