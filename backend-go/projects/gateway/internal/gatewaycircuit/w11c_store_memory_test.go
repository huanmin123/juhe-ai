package gatewaycircuit

// w11c: memory-store validation, mismatch, escalation and restore paths.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func w11cMemStore(t *testing.T, capacity int64, now int64) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: capacity, Now: func() int64 { return now }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

func TestW11CMemoryStoreOptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		options MemoryStoreOptions
	}{
		{"capacity", MemoryStoreOptions{Capacity: 0}},
		{"closedRetentionMs", MemoryStoreOptions{Capacity: 10, ClosedRetentionMs: -1}},
		{"replayLimitPerScope", MemoryStoreOptions{Capacity: 10, ReplayLimitPerScope: -1}},
	}
	for _, tc := range cases {
		if _, err := NewMemoryStore(tc.options); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
	// Zero-valued closed retention / replay limit fall back to defaults and a
	// missing clock/random fall back to the package defaults.
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 5})
	if err != nil {
		t.Fatalf("NewMemoryStore defaults: %v", err)
	}
	if store.closedRetentionMs <= 0 || store.replayLimitPerScope <= 0 || store.now == nil || store.random == nil {
		t.Fatalf("defaults not applied: %+v", store)
	}
}

func TestW11CMemorySuspectValidationErrors(t *testing.T) {
	store := w11cMemStore(t, 10, 1_000)
	scope := protocolScope("acc")
	// Invalid confirmation failures required propagates the raw error.
	bad := int64(-1)
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "t1", ConfirmationFailuresRequired: &bad,
	}); err == nil {
		t.Fatalf("expected confirmation failures validation error")
	}
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "", TransitionID: "t1",
	}); err == nil {
		t.Fatalf("expected empty dispatchRevision error")
	}
	if _, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: " ",
	}); err == nil {
		t.Fatalf("expected empty transitionID error")
	}
	// Existing suspect in a different revision is stale.
	mustSuspect(t, store, scope, "7", "t-ok", 1_000)
	stale, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "9", TransitionID: "t2",
	})
	if err != nil || stale.Status != MutationStaleDispatchRevision {
		t.Fatalf("stale suspect = (%s, %v)", stale.Status, err)
	}
	// A replayed transition id is idempotent.
	replay := mustSuspect(t, store, scope, "7", "t-ok", 1_000)
	if replay.Status != MutationIdempotent {
		t.Fatalf("replay = %s", replay.Status)
	}
}

func TestW11CMemoryAcquireConfirmationGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 10, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Missing entry -> not found.
	missing, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: "7", TransitionID: "a0", LeaseID: "l0", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || missing.Status != MutationNotFound {
		t.Fatalf("missing = (%s, %v)", missing.Status, err)
	}
	// Generation mismatch -> stale generation.
	staleGen, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 9, DispatchRevision: "7", TransitionID: "a1", LeaseID: "l1", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || staleGen.Status != MutationStaleGeneration {
		t.Fatalf("staleGen = (%s, %v)", staleGen.Status, err)
	}
	// Revision mismatch -> stale dispatch revision.
	staleRev, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "8", TransitionID: "a2", LeaseID: "l2", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || staleRev.Status != MutationStaleDispatchRevision {
		t.Fatalf("staleRev = (%s, %v)", staleRev.Status, err)
	}
	// Wrong expected evidence key -> state mismatch.
	wrongEvidence := strings.Repeat("d", 64)
	mismatch, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a3",
		ExpectedFailureEvidenceKey: &wrongEvidence, LeaseID: "l3", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("evidence mismatch = (%s, %v)", mismatch.Status, err)
	}
	// Not due before retryAt elapses.
	notDue, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a4", LeaseID: "l4", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || notDue.Status != MutationNotDue {
		t.Fatalf("notDue = (%s, %v)", notDue.Status, err)
	}
	// Empty lease id errors once the suspect window has elapsed.
	pastWindow := now + 10_000
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a5", LeaseID: " ", LeaseUntilMs: pastWindow + 5_000, NowMs: &pastWindow,
	}); err == nil {
		t.Fatalf("expected lease id validation error")
	}
	// Lease until in the past errors.
	if _, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a6", LeaseID: "l6", LeaseUntilMs: pastWindow, NowMs: &pastWindow,
	}); err == nil {
		t.Fatalf("expected lease deadline validation error")
	}

	// After retryAt: acquire with a fresh confirmation evidence key.
	due := pastWindow
	evidence := strings.Repeat("b", 64)
	acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a7",
		ConfirmationEvidenceKey: &evidence, LeaseID: "l7", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquired = (%s, %v)", acquired.Status, err)
	}
	// A second acquire while the lease is live -> state mismatch.
	conflict, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a8",
		ConfirmationEvidenceKey: strPtr(strings.Repeat("e", 64)), LeaseID: "l8", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || conflict.Status != MutationStateMismatch {
		t.Fatalf("lease conflict = (%s, %v)", conflict.Status, err)
	}
}

func TestW11CMemoryCompleteConfirmationGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 10, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Missing entry -> not found.
	missing, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: "7", TransitionID: "c0", LeaseID: "l0", Outcome: OutcomeFramingComplete, NowMs: &now,
	})
	if err != nil || missing.Status != MutationNotFound {
		t.Fatalf("missing = (%s, %v)", missing.Status, err)
	}
	// Stale generation.
	stale, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: 5, DispatchRevision: "7", TransitionID: "c1", LeaseID: "l1", Outcome: OutcomeFramingComplete, NowMs: &now,
	})
	if err != nil || stale.Status != MutationStaleGeneration {
		t.Fatalf("stale = (%s, %v)", stale.Status, err)
	}
	// No lease -> lease mismatch.
	noLease, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "c2", LeaseID: "l2", Outcome: OutcomeFramingComplete, NowMs: &now,
	})
	if err != nil || noLease.Status != MutationLeaseMismatch {
		t.Fatalf("noLease = (%s, %v)", noLease.Status, err)
	}
	// Acquire then complete with the wrong lease id.
	due := now + 5_000
	acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "a1", LeaseID: "lease-1", LeaseUntilMs: due + 30_000, NowMs: &due,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	wrongLease, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "c3", LeaseID: "lease-2", Outcome: OutcomeFramingComplete, NowMs: &due,
	})
	if err != nil || wrongLease.Status != MutationLeaseMismatch {
		t.Fatalf("wrongLease = (%s, %v)", wrongLease.Status, err)
	}
	// Replay of a completed transition is idempotent.
	framed, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "c4", LeaseID: "lease-1", Outcome: OutcomeFramingComplete, NowMs: &due,
	})
	if err != nil || framed.Status != MutationApplied || framed.State.Phase != PhaseRecovering {
		t.Fatalf("framed = (%s, %+v, %v)", framed.Status, framed.State.Phase, err)
	}
	replay, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "c4", LeaseID: "lease-1", Outcome: OutcomeFramingComplete, NowMs: &due,
	})
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("replay = (%s, %v)", replay.Status, err)
	}
	// Framing with disposition closed closes the circuit.
	suspect2 := mustSuspect(t, store, protocolScope("acc2"), "7", "t2", now)
	due2 := now + 5_000
	acquired2, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
		Scope: protocolScope("acc2"), Generation: suspect2.State.Generation, DispatchRevision: "7", TransitionID: "a2", LeaseID: "lease-3", LeaseUntilMs: due2 + 30_000, NowMs: &due2,
	})
	if err != nil || acquired2.Status != MutationApplied {
		t.Fatalf("acquire2 = (%s, %v)", acquired2.Status, err)
	}
	closed := "closed"
	framedClosed, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
		Scope: protocolScope("acc2"), Generation: suspect2.State.Generation, DispatchRevision: "7", TransitionID: "c5", LeaseID: "lease-3",
		Outcome: OutcomeFramingComplete, FramingCompleteDisposition: &closed, NowMs: &due2,
	})
	if err != nil || framedClosed.Status != MutationApplied || framedClosed.State.Phase != PhaseClosed {
		t.Fatalf("framedClosed = (%s, %v)", framedClosed.Status, err)
	}
}

func TestW11CMemoryCloseSuspectFromObserverAndKeyRotationGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 10, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Missing entry.
	missing, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: "7", TransitionID: "x0", ObserverEvidenceKey: strings.Repeat("b", 64), NowMs: &now,
	})
	if err != nil || missing.Status != MutationNotFound {
		t.Fatalf("observer missing = (%s, %v)", missing.Status, err)
	}
	missingRotation, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: "7", TransitionID: "y0", NowMs: &now,
	})
	if err != nil || missingRotation.Status != MutationNotFound {
		t.Fatalf("rotation missing = (%s, %v)", missingRotation.Status, err)
	}
	// Stale generation through the observer path.
	stale, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: scope, Generation: 9, DispatchRevision: "7", TransitionID: "x1", ObserverEvidenceKey: strings.Repeat("b", 64), NowMs: &now,
	})
	if err != nil || stale.Status != MutationStaleGeneration {
		t.Fatalf("observer stale = (%s, %v)", stale.Status, err)
	}
	// Wrong expected evidence -> mismatch.
	wrong := strings.Repeat("d", 64)
	mismatch, err := store.CloseSuspectFromObserver(context.Background(), CloseSuspectFromObserverInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "x2", ObserverEvidenceKey: strings.Repeat("b", 64),
		ExpectedFailureEvidenceKey: wrong, NowMs: &now,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("observer mismatch = (%s, %v)", mismatch.Status, err)
	}
	// Key rotation close with the wrong expected evidence -> mismatch.
	rotationMismatch, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "y1",
		ExpectedFailureEvidenceKey: wrong, NowMs: &now,
	})
	if err != nil || rotationMismatch.Status != MutationStateMismatch {
		t.Fatalf("rotation mismatch = (%s, %v)", rotationMismatch.Status, err)
	}
	// Successful key-rotation close.
	keys, err := FailureEvidenceKeysOf(suspect.State)
	if err != nil || len(keys) == 0 {
		t.Fatalf("evidence keys = (%v, %v)", keys, err)
	}
	expected := keys[len(keys)-1]
	closed, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "y2",
		ExpectedFailureEvidenceKey: expected, NowMs: &now,
	})
	if err != nil || closed.Status != MutationApplied || closed.State.Phase != PhaseClosed {
		t.Fatalf("rotation closed = (%s, %+v, %v)", closed.Status, closed.State.Phase, err)
	}
	// Replay of the same transition is idempotent.
	replay, err := store.CloseSuspectFromKeyRotation(context.Background(), CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "y2",
		ExpectedFailureEvidenceKey: expected, NowMs: &now,
	})
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("rotation replay = (%s, %v)", replay.Status, err)
	}
}

func TestW11CMemoryCanaryGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 10, now)
	scope := protocolScope("acc")

	// Missing entry -> not found.
	missing, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k0", LeaseID: "c0", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || missing.Status != MutationNotFound {
		t.Fatalf("canary missing = (%s, %v)", missing.Status, err)
	}
	// A suspect (not open/recovering) state cannot host a canary.
	suspect := mustSuspect(t, store, scope, "7", "t1", now)
	mismatch, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", TransitionID: "k1", LeaseID: "c1", LeaseUntilMs: now + 5_000, NowMs: &now,
	})
	if err != nil || mismatch.Status != MutationStateMismatch {
		t.Fatalf("canary mismatch = (%s, %v)", mismatch.Status, err)
	}
	// Open the circuit through two confirmed transport failures.
	openState := w11cOpenScope(t, store, scope, "7", now)
	rev := "7"
	// Not due before retryAt elapses.
	if openState.RetryAtMs == nil {
		t.Fatalf("open state missing retryAt")
	}
	beforeDue := *openState.RetryAtMs - 1
	notDue, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: openState.Generation, DispatchRevision: rev, TransitionID: "k2", LeaseID: "c2", LeaseUntilMs: beforeDue + 5_000, NowMs: &beforeDue,
	})
	if err != nil || notDue.Status != MutationNotDue {
		t.Fatalf("canary not due = (%s, %v)", notDue.Status, err)
	}
	// CompleteCanary guards on a missing entry / wrong phase / wrong lease.
	canaryMissing, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: rev, TransitionID: "kc0", LeaseID: "c9", Outcome: OutcomeFramingComplete, NowMs: &beforeDue,
	})
	if err != nil || canaryMissing.Status != MutationNotFound {
		t.Fatalf("complete canary missing = (%s, %v)", canaryMissing.Status, err)
	}
	stale := int64(77)
	staleResult, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: stale, DispatchRevision: rev, TransitionID: "kc1", LeaseID: "c9", Outcome: OutcomeFramingComplete, NowMs: &beforeDue,
	})
	if err != nil || staleResult.Status != MutationStaleGeneration {
		t.Fatalf("complete canary stale = (%s, %v)", staleResult.Status, err)
	}
	phaseMismatch, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: openState.Generation, DispatchRevision: rev, TransitionID: "kc2", LeaseID: "c9", Outcome: OutcomeFramingComplete, NowMs: &beforeDue,
	})
	if err != nil || phaseMismatch.Status != MutationStateMismatch {
		t.Fatalf("complete canary phase = (%s, %v)", phaseMismatch.Status, err)
	}
	// Advance the clock past retryAt and acquire the half-open canary.
	readyAt := now + 60_000
	acquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: openState.Generation, DispatchRevision: rev, TransitionID: "k3", LeaseID: "c3", LeaseUntilMs: readyAt + 5_000, NowMs: &readyAt,
	})
	if err != nil || acquired.Status != MutationApplied || acquired.State.Phase != PhaseHalfOpen {
		t.Fatalf("canary acquired = (%s, %+v, %v)", acquired.Status, acquired.State.Phase, err)
	}
	// Wrong lease id on completion.
	wrongLease, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: rev, TransitionID: "kc3", LeaseID: "cX", Outcome: OutcomeFramingComplete, NowMs: &readyAt,
	})
	if err != nil || wrongLease.Status != MutationLeaseMismatch {
		t.Fatalf("complete canary wrong lease = (%s, %v)", wrongLease.Status, err)
	}
	// Unknown outcome restores the open origin with a longer backoff.
	restored, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: acquired.State.Generation, DispatchRevision: rev, TransitionID: "kc4", LeaseID: "c3", Outcome: OutcomeUnknown, NowMs: &readyAt,
	})
	if err != nil || restored.Status != MutationApplied || restored.State.Phase != PhaseOpen {
		t.Fatalf("canary unknown = (%s, %+v, %v)", restored.Status, restored.State.Phase, err)
	}
	// Transport failure through the canary re-opens directly.
	late := now + 150_000
	reacquired, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: restored.State.Generation, DispatchRevision: rev, TransitionID: "k4", LeaseID: "c4", LeaseUntilMs: late + 5_000, NowMs: &late,
	})
	if err != nil || reacquired.Status != MutationApplied {
		t.Fatalf("canary reacquired = (%s, %v)", reacquired.Status, err)
	}
	reopened, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: reacquired.State.Generation, DispatchRevision: rev, TransitionID: "kc5", LeaseID: "c4", Outcome: OutcomeTransportFailure, NowMs: &late,
	})
	if err != nil || reopened.Status != MutationApplied || reopened.State.Phase != PhaseOpen {
		t.Fatalf("canary transport = (%s, %+v, %v)", reopened.Status, reopened.State.Phase, err)
	}
	// A framing-complete canary from the open origin enters recovering.
	finalAt := now + 250_000
	final, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: reopened.State.Generation, DispatchRevision: rev, TransitionID: "k5", LeaseID: "c5", LeaseUntilMs: finalAt + 5_000, NowMs: &finalAt,
	})
	if err != nil || final.Status != MutationApplied {
		t.Fatalf("canary final = (%s, %v)", final.Status, err)
	}
	recovering, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: final.State.Generation, DispatchRevision: rev, TransitionID: "kc6", LeaseID: "c5", Outcome: OutcomeFramingComplete, NowMs: &finalAt,
	})
	if err != nil || recovering.Status != MutationApplied || recovering.State.Phase != PhaseRecovering {
		t.Fatalf("canary framing = (%s, %+v, %v)", recovering.Status, recovering.State.Phase, err)
	}
	// A recovering-origin canary success accumulates recovery evidence.
	relaxed := now + 400_000
	c2, err := store.AcquireCanaryLease(context.Background(), AcquireCanaryLeaseInput{
		Scope: scope, Generation: recovering.State.Generation, DispatchRevision: rev, TransitionID: "k6", LeaseID: "c6", LeaseUntilMs: relaxed + 5_000, NowMs: &relaxed,
	})
	if err != nil || c2.Status != MutationApplied {
		t.Fatalf("recovering canary = (%s, %v)", c2.Status, err)
	}
	recovering2, err := store.CompleteCanary(context.Background(), CompleteCanaryInput{
		Scope: scope, Generation: c2.State.Generation, DispatchRevision: rev, TransitionID: "kc7", LeaseID: "c6", Outcome: OutcomeFramingComplete, NowMs: &relaxed,
	})
	if err != nil || recovering2.Status != MutationApplied || recovering2.State.Phase != PhaseRecovering {
		t.Fatalf("recovering canary complete = (%s, %+v, %v)", recovering2.Status, recovering2.State.Phase, err)
	}
	if recovering2.State.RecoverySuccessCount != recovering.State.RecoverySuccessCount+1 {
		t.Fatalf("recovery successes = %d, want %d", recovering2.State.RecoverySuccessCount, recovering.State.RecoverySuccessCount+1)
	}
}

func TestW11CMemoryEscalationValidationAndGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Missing child entry.
	missing, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: protocolScope("other"), Generation: 1, DispatchRevision: "7", EvidenceID: "e0", AccountTransitionID: "p0", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	})
	if err != nil || missing.Status != EscalationNotFound {
		t.Fatalf("escalation missing = (%s, %v)", missing.Status, err)
	}
	// Stale generation.
	staleGen, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: 9, DispatchRevision: "7", EvidenceID: "e1", AccountTransitionID: "p1", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	})
	if err != nil || staleGen.Status != EscalationStaleGeneration {
		t.Fatalf("escalation stale gen = (%s, %v)", staleGen.Status, err)
	}
	// Stale revision.
	staleRev, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "8", EvidenceID: "e2", AccountTransitionID: "p2", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	})
	if err != nil || staleRev.Status != EscalationStaleRevision {
		t.Fatalf("escalation stale rev = (%s, %v)", staleRev.Status, err)
	}
	// Child not open yet.
	stateMismatch, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: suspect.State.Generation, DispatchRevision: "7", EvidenceID: "e3", AccountTransitionID: "p3", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	})
	if err != nil || stateMismatch.Status != EscalationStateMismatch {
		t.Fatalf("escalation mismatch = (%s, %v)", stateMismatch.Status, err)
	}
	// Validation errors once the child is OPEN.
	opened := w11cOpenScope(t, store, scope, "7", now)
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e4", AccountTransitionID: "p4", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 0, MaxProtocolScopes: 8, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected windowMs validation error")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e5", AccountTransitionID: "p5", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 0, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected maxProtocolScopes validation error")
	}
	badThreshold := int64(-3)
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e6", AccountTransitionID: "p6", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: badThreshold, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected threshold validation error")
	}
	// Threshold above maxProtocolScopes.
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e7", AccountTransitionID: "p7", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 20, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected threshold/max validation error")
	}
	// Missing confirmed failure count.
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e10", AccountTransitionID: "p10", Reason: "r",
		ConfirmedFailureCount: 0, DistinctScopeThreshold: 2, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected confirmedFailureCount validation error")
	}
	// Missing account transition id only fails once the distinct scope threshold
	// is reached (three distinct open protocol scopes, account incident absent).
	w11cRecordEvidenceForScopes(t, store, "acc", "7", now, 2, "w11c-ev-a")
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7", EvidenceID: "e8", AccountTransitionID: " ", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	}); err == nil {
		t.Fatalf("expected accountTransitionId validation error")
	}
}

// w11cRecordEvidenceForScopes opens `count` distinct protocol-model scopes under
// one account runtime key and records one open evidence for each.
func w11cRecordEvidenceForScopes(t *testing.T, store *MemoryStore, runtimeKey, revision string, now int64, count int, evidencePrefix string) []State {
	t.Helper()
	for index := 0; index < count; index++ {
		scope := Scope{
			Kind: ScopeKindProtocolModel, AccountRuntimeKey: runtimeKey, ProtocolProfile: "profile",
			RequestLane: LaneText, ModelBucket: fmt.Sprintf("w11c-model-%d", index),
		}
		opened := w11cOpenScope(t, store, scope, revision, now)
		result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
			Scope: scope, Generation: opened.Generation, DispatchRevision: revision,
			EvidenceID:          fmt.Sprintf("%s-%d", evidencePrefix, index),
			AccountTransitionID: "w11c-parent", Reason: "protocol_model_transport_failure",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
		})
		if err != nil {
			t.Fatalf("record evidence %d: %v", index, err)
		}
		if result.Status != EscalationRecorded && result.Status != EscalationEscalated {
			t.Fatalf("record evidence %d status = %s", index, result.Status)
		}
	}
	return nil
}

// w11cOpenScope drives a protocol-model scope to OPEN through confirmed
// transport failures, advancing the clock past each retry window.
func w11cOpenScope(t *testing.T, store *MemoryStore, scope Scope, revision string, now int64) State {
	t.Helper()
	attempt := 0
	for {
		attempt++
		if attempt > 6 {
			t.Fatalf("scope never opened")
		}
		due := now + int64(5_000*attempt)
		state, err := store.Get(context.Background(), scope, &due)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if state.Phase == PhaseOpen {
			return state
		}
		if state.Phase != PhaseSuspect {
			// Bootstrap the suspect incident when the scope is still closed; the
			// next loop iteration advances the clock past its retry window.
			bootstrap := mustSuspect(t, store, scope, revision, "w11c-bootstrap", due)
			if bootstrap.Status != MutationApplied {
				t.Fatalf("bootstrap suspect = %s", bootstrap.Status)
			}
			continue
		}
		leap := string(rune('0' + attempt))
		acquired, err := store.AcquireConfirmationLease(context.Background(), AcquireConfirmationLeaseInput{
			Scope: scope, Generation: state.Generation, DispatchRevision: revision,
			TransitionID: "w11c-a-" + leap, LeaseID: "w11c-lease-" + leap,
			LeaseUntilMs: due + 30_000, NowMs: &due,
		})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if acquired.Status != MutationApplied {
			t.Fatalf("acquire status = %s", acquired.Status)
		}
		evidence := strings.Repeat(string(rune('g'+attempt)), 64)
		completed, err := store.CompleteConfirmation(context.Background(), CompleteConfirmationInput{
			Scope: scope, Generation: acquired.State.Generation, DispatchRevision: revision,
			TransitionID: "w11c-c-" + leap, LeaseID: "w11c-lease-" + leap,
			Outcome: OutcomeTransportFailure, FailureEvidenceKey: &evidence, NowMs: &due,
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if completed.State.Phase == PhaseOpen {
			return completed.State
		}
	}
}

func TestW11CMemoryEscalationIdempotentAndRecorded(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	opened := w11cOpenScope(t, store, scope, "7", now)

	base := ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7",
		AccountTransitionID: "w11c-parent-1", Reason: "protocol_model_transport_failure",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 20, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	}
	// Below the threshold the evidence is recorded without escalation.
	base.EvidenceID = "w11c-ev-1"
	recorded, err := store.RecordProtocolModelOpenEvidence(context.Background(), base)
	if err != nil || recorded.Status != EscalationRecorded {
		t.Fatalf("recorded = (%s, %v)", recorded.Status, err)
	}
	// Replaying the same evidence id is idempotent.
	again, err := store.RecordProtocolModelOpenEvidence(context.Background(), base)
	if err != nil || again.Status != EscalationIdempotent {
		t.Fatalf("idempotent = (%s, %v)", again.Status, err)
	}
	// Clearing the evidence with a mismatched revision is a no-op.
	cleared, err := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "8", EvidenceID: "w11c-clear", NowMs: &now,
	})
	if err != nil || cleared {
		t.Fatalf("clear mismatched = (%v, %v)", cleared, err)
	}
	// Clearing with the matching revision succeeds.
	cleared, err = store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "7", EvidenceID: "w11c-clear", NowMs: &now,
	})
	if err != nil || !cleared {
		t.Fatalf("clear = (%v, %v)", cleared, err)
	}
	// Validation errors on clear.
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: " ", DispatchRevision: "7", EvidenceID: "w11c-clear", NowMs: &now,
	}); err == nil {
		t.Fatalf("expected accountRuntimeKey validation error")
	}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: " ", EvidenceID: "w11c-clear", NowMs: &now,
	}); err == nil {
		t.Fatalf("expected dispatchRevision validation error")
	}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "7", EvidenceID: "", NowMs: &now,
	}); err == nil {
		t.Fatalf("expected evidenceID validation error")
	}
}

func TestW11CMemoryEscalationAttachesAndShadows(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	opened := w11cOpenScope(t, store, scope, "7", now)

	// Two recorded scopes stay below the threshold.
	if result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7",
		EvidenceID: "w11c-ev-0", AccountTransitionID: "w11c-parent-1", Reason: "protocol_model_transport_failure",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	}); err != nil || result.Status != EscalationRecorded {
		t.Fatalf("recorded = (%s, %v)", result.Status, err)
	}
	second := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-second"}
	openedSecond := w11cOpenScope(t, store, second, "7", now)
	if result, err := store.RecordProtocolModelOpenEvidence(context.Background(), ProtocolModelOpenEvidenceInput{
		Scope: second, Generation: openedSecond.Generation, DispatchRevision: "7",
		EvidenceID: "w11c-ev-1", AccountTransitionID: "w11c-parent-1", Reason: "protocol_model_transport_failure",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	}); err != nil || result.Status != EscalationRecorded {
		t.Fatalf("recorded second = (%s, %v)", result.Status, err)
	}
	// The third distinct scope crosses the threshold and escalates the account.
	input := ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: opened.Generation, DispatchRevision: "7",
		AccountTransitionID: "w11c-parent-1", Reason: "protocol_model_transport_failure",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 32, NowMs: &now,
	}
	input.EvidenceID = "w11c-ev-2"
	third := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "profile", RequestLane: LaneText, ModelBucket: "w11c-third"}
	openedThird := w11cOpenScope(t, store, third, "7", now)
	input.Scope = third
	input.Generation = openedThird.Generation
	escalated, err := store.RecordProtocolModelOpenEvidence(context.Background(), input)
	if err != nil || escalated.Status != EscalationEscalated {
		t.Fatalf("escalated = (%s, %v)", escalated.Status, err)
	}
	if escalated.AccountState.Phase != PhaseOpen || escalated.AccountState.Scope.Kind != ScopeKindAccount {
		t.Fatalf("account state = %+v", escalated.AccountState)
	}
	// The protocol children are shadowed by the account incident.
	child, err := store.Get(context.Background(), scope, &now)
	if err != nil || child.ShadowedByIncidentID == nil || *child.ShadowedByIncidentID != "w11c-parent-1" {
		t.Fatalf("child shadow = (%v, %v)", child.ShadowedByIncidentID, err)
	}
	// A further distinct evidence on the same scope reports already-active and
	// keeps the account incident.
	input.EvidenceID = "w11c-ev-3"
	active, err := store.RecordProtocolModelOpenEvidence(context.Background(), input)
	if err != nil || active.Status != EscalationAlreadyActive {
		t.Fatalf("already active = (%s, %v)", active.Status, err)
	}
	// Drive the account scope to closed through the public Restore path with a
	// newer generation so the unshadow projection runs.
	accountState, err := store.Get(context.Background(), escalated.AccountState.Scope, int64Ptr(now+120_000))
	if err != nil {
		t.Fatalf("account get: %v", err)
	}
	closing := accountState
	closing.Phase = PhaseClosed
	closing.Generation = accountState.Generation + 1
	closing.UpdatedAtMs = now + 130_000
	closing.TransitionID = "w11c-account-close"
	restored, err := store.Restore(context.Background(), closing, int64Ptr(now+130_000))
	if err != nil || restored.Status != MutationApplied {
		t.Fatalf("restore close = (%s, %v)", restored.Status, err)
	}
	unshadowed := false
	scopeKeyOfChild := MustScopeKey(scope)
	for _, related := range restored.RelatedStates.Slice() {
		if related.ScopeKey == scopeKeyOfChild && related.ShadowedByIncidentID == nil {
			unshadowed = true
		}
	}
	if !unshadowed {
		t.Fatalf("expected unshadowed child in related states: %v", restored.RelatedStates)
	}
}

func TestW11CMemoryReplaceDispatchRevisionGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Same revision is idempotent (and does not remember the transition).
	same, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "r1", NowMs: &now,
	})
	if err != nil || same.Status != MutationIdempotent {
		t.Fatalf("same revision = (%s, %v)", same.Status, err)
	}
	// A newer revision resets the scope to closed and remembers the transition.
	newer, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "12", TransitionID: "r3", NowMs: &now,
	})
	if err != nil || newer.Status != MutationApplied || newer.State.Phase != PhaseClosed {
		t.Fatalf("newer = (%s, %+v, %v)", newer.Status, newer.State.Phase, err)
	}
	// Replay of an applied transition id is idempotent.
	replay, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "13", TransitionID: "r3", NowMs: &now,
	})
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("replay = (%s, %v)", replay.Status, err)
	}
	// An older numeric revision is stale.
	older, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "3", TransitionID: "r2", NowMs: &now,
	})
	if err != nil || older.Status != MutationStaleDispatchRevision {
		t.Fatalf("older = (%s, %v)", older.Status, err)
	}
	// Validation errors.
	if _, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: " ", TransitionID: "r4", NowMs: &now,
	}); err == nil {
		t.Fatalf("expected dispatchRevision validation error")
	}
	if _, err := store.ReplaceDispatchRevision(context.Background(), ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "13", TransitionID: "", NowMs: &now,
	}); err == nil {
		t.Fatalf("expected transitionID validation error")
	}
	_ = suspect
}

func TestW11CMemoryRestoreGuards(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	suspect := mustSuspect(t, store, scope, "7", "t1", now)

	// Scope-key mismatch is rejected.
	bad := suspect.State
	bad.Scope = protocolScope("other")
	if _, err := store.Restore(context.Background(), bad, &now); err == nil {
		t.Fatalf("expected scope key assertion error")
	}
	// An older numeric revision is stale.
	older := suspect.State
	older.DispatchRevision = "3"
	older.Generation = suspect.State.Generation + 1
	stale, err := store.Restore(context.Background(), older, &now)
	if err != nil || stale.Status != MutationStaleDispatchRevision {
		t.Fatalf("stale restore = (%s, %v)", stale.Status, err)
	}
	// Restoring the same generation at an older timestamp is idempotent.
	same := suspect.State
	same.UpdatedAtMs = suspect.State.UpdatedAtMs - 1
	idempotent, err := store.Restore(context.Background(), same, &now)
	if err != nil || idempotent.Status != MutationIdempotent {
		t.Fatalf("idempotent restore = (%s, %v)", idempotent.Status, err)
	}
	// A newer generation applies.
	newer := suspect.State
	newer.Generation = suspect.State.Generation + 1
	newer.UpdatedAtMs = now + 1
	newer.TransitionID = "w11c-newer"
	applied, err := store.Restore(context.Background(), newer, &now)
	if err != nil || applied.Status != MutationApplied {
		t.Fatalf("applied restore = (%s, %v)", applied.Status, err)
	}
}

func TestW11CMemorySizeAndListDueValidation(t *testing.T) {
	now := int64(1_000)
	store := w11cMemStore(t, 100, now)
	scope := protocolScope("acc")
	mustSuspect(t, store, scope, "7", "t1", now)
	size, err := store.Size(context.Background())
	if err != nil || size != 1 {
		t.Fatalf("size = (%d, %v)", size, err)
	}
	if _, err := store.ListDue(context.Background(), now, 0); err == nil {
		t.Fatalf("expected limit validation error")
	}
	due, err := store.ListDue(context.Background(), now+10_000, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("listDue = (%d, %v)", len(due), err)
	}
}
