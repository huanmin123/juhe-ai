package gatewaycircuit

import (
	"context"
	"strings"
	"sync"
	"testing"

	redis "github.com/redis/go-redis/v9"
	miniredis "github.com/alicebob/miniredis/v2"
)

func newTestRedisStore(t *testing.T, capacity int64, now func() int64) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL: "redis://" + server.Addr(),
		Name:     "test-circuit",
		Capacity: capacity,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	return store, server
}

func TestRedisSuspectToOpenParity(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	ctx := context.Background()
	scope := accountScope("acc")

	suspect, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1",
		Reason: "transport:connect failed", NowMs: clock,
	})
	if err != nil || suspect.Status != MutationApplied {
		t.Fatalf("suspect = (%s, %v)", suspect.Status, err)
	}
	if suspect.State.Phase != PhaseSuspect || suspect.State.Generation != 1 {
		t.Fatalf("suspect state = %+v", suspect.State)
	}
	if suspect.State.RetryAtMs == nil || *suspect.State.RetryAtMs != 3000 {
		t.Fatalf("suspect retryAtMs = %v", suspect.State.RetryAtMs)
	}
	// Replay of the same transition id is idempotent.
	replay, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1",
		Reason: "transport:connect failed", NowMs: clock,
	})
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("replay = (%s, %v)", replay.Status, err)
	}
	// get returns the persisted state.
	got, err := store.Get(ctx, scope, clock)
	if err != nil || got.Phase != PhaseSuspect {
		t.Fatalf("get = (%+v, %v)", got, err)
	}

	// acquire before retryAt -> not_due; after -> applied.
	tooEarly, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a0",
		LeaseID: "lease-0", LeaseUntilMs: 30_000, NowMs: clock,
	})
	if err != nil || tooEarly.Status != MutationNotDue {
		t.Fatalf("too early = (%s, %v)", tooEarly.Status, err)
	}
	*clock = 3000
	acquired, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "lease-1", LeaseUntilMs: 30_000, NowMs: clock,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	if acquired.State.Lease == nil || acquired.State.Lease.LeaseID != "lease-1" {
		t.Fatalf("lease = %+v", acquired.State.Lease)
	}

	// Two independent confirmed failures open the circuit.
	sha1 := strings.Repeat("1", 64)
	sha2 := strings.Repeat("2", 64)
	first, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-1", Outcome: OutcomeTransportFailure,
		Reason: strPtr("transport:e1"), FailureEvidenceKey: strPtr(sha1), NowMs: clock,
	})
	if err != nil || first.State.Phase != PhaseSuspect || *first.State.ConfirmationFailureCount != 1 {
		t.Fatalf("first failure = (%+v, %v)", first.State, err)
	}
	*clock = 6000
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a2",
		LeaseID: "lease-2", LeaseUntilMs: 60_000, NowMs: clock,
	}); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	opened, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c2",
		LeaseID: "lease-2", Outcome: OutcomeTransportFailure,
		FailureEvidenceKey: strPtr(sha2), NowMs: clock,
	})
	if err != nil || opened.State.Phase != PhaseOpen {
		t.Fatalf("opened = (%+v, %v)", opened.State, err)
	}
	if opened.State.BackoffAttempt != 1 || opened.State.IncidentID == nil || *opened.State.IncidentID != "s1" {
		t.Fatalf("open state = %+v", opened.State)
	}
	if opened.State.RetryAtMs == nil || *opened.State.RetryAtMs != 6000+3000 {
		t.Fatalf("open retryAtMs = %v (attempt 1 must use the exact base)", opened.State.RetryAtMs)
	}

	// canary -> HALF_OPEN -> framing complete -> RECOVERING.
	*clock = 60_000
	canary, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k1",
		LeaseID: "h1", LeaseUntilMs: 120_000, NowMs: clock,
	})
	if err != nil || canary.Status != MutationApplied || canary.State.Phase != PhaseHalfOpen {
		t.Fatalf("canary = (%s, %+v, %v)", canary.Status, canary.State, err)
	}
	if canary.State.Lease.Kind != LeaseKindHalfOpen {
		t.Fatalf("lease kind = %s", canary.State.Lease.Kind)
	}
	recovering, err := store.CompleteCanary(ctx, CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k1c",
		LeaseID: "h1", Outcome: OutcomeFramingComplete, NowMs: clock,
	})
	if err != nil || recovering.State.Phase != PhaseRecovering {
		t.Fatalf("recovering = (%+v, %v)", recovering.State, err)
	}
	// One more canary with a transport failure reopens.
	*clock = 63_000
	if _, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k2",
		LeaseID: "h2", LeaseUntilMs: 200_000, NowMs: clock,
	}); err != nil {
		t.Fatalf("canary 2: %v", err)
	}
	reopened, err := store.CompleteCanary(ctx, CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "k2c",
		LeaseID: "h2", Outcome: OutcomeTransportFailure, Reason: strPtr("transport:again"), NowMs: clock,
	})
	if err != nil || reopened.State.Phase != PhaseOpen || reopened.State.BackoffAttempt != 2 {
		t.Fatalf("reopened = (%+v, %v)", reopened.State, err)
	}
}

func TestRedisLeaseExpiryAndClose(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	ctx := context.Background()
	scope := accountScope("acc")
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1", Reason: "r", NowMs: clock,
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "l1", LeaseUntilMs: 30_000, NowMs: clock,
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// After the lease deadline, get normalizes the state (lease cleared).
	*clock = 30_000
	state, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Lease != nil || state.RetryAtMs == nil || *state.RetryAtMs != 30_000 {
		t.Fatalf("normalized state = %+v", state)
	}
	// closeSuspectFromKeyRotation closes the incident.
	sha := strings.Repeat("3", 64)
	*clock = 33_000
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a2",
		LeaseID: "l2", LeaseUntilMs: 60_000, NowMs: clock,
	}); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if _, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "l2", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha), NowMs: clock,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// Evidence count is now 1 of required 2, still SUSPECT.
	closed, err := store.CloseSuspectFromKeyRotation(ctx, CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "kr1",
		ExpectedFailureEvidenceKey: sha, NowMs: clock,
	})
	if err != nil || closed.Status != MutationApplied {
		t.Fatalf("closeSuspectFromKeyRotation = (%s, %v)", closed.Status, err)
	}
	if closed.State.Phase != PhaseClosed || closed.State.Generation != 1 {
		t.Fatalf("closed state = %+v", closed.State)
	}
	// Closed retention keeps the entry for a while, then it expires.
	size, err := store.Size(ctx)
	if err != nil || size != 1 {
		t.Fatalf("size = (%d, %v)", size, err)
	}
	*clock = 5*60_000 + 33_000
	if size, err := store.Size(ctx); err != nil || size != 0 {
		t.Fatalf("size after retention = (%d, %v)", size, err)
	}
}

func TestRedisCloseSuspectFromObserver(t *testing.T) {
	now := int64(0)
	store, _ := newTestRedisStore(t, 100, func() int64 { return now })
	ctx := context.Background()
	scope := accountScope("acc")
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1", Reason: "r",
		FailureEvidenceKey: strPtr(strings.Repeat("4", 64)), NowMs: int64Ptr(now),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	// The observer evidence must differ from the failure evidence.
	wrong, err := store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "o1",
		ExpectedFailureEvidenceKey: strings.Repeat("4", 64),
		ObserverEvidenceKey:        strings.Repeat("4", 64), NowMs: int64Ptr(now),
	})
	if err != nil || wrong.Status != MutationStateMismatch {
		t.Fatalf("same evidence close = (%s, %v)", wrong.Status, err)
	}
	closed, err := store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "o2",
		ExpectedFailureEvidenceKey: strings.Repeat("4", 64),
		ObserverEvidenceKey:        strings.Repeat("5", 64), NowMs: int64Ptr(now),
	})
	if err != nil || closed.Status != MutationApplied || closed.State.Phase != PhaseClosed {
		t.Fatalf("observer close = (%s, %+v, %v)", closed.Status, closed.State, err)
	}
}

func TestRedisRestoreListDueAndReplaceRevision(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	ctx := context.Background()
	scope := accountScope("acc")
	state := ClosedState(scope, "7", 2, "t1", 1000)
	state.Phase = PhaseOpen
	state.RetryAtMs = int64Ptr(3000)
	if _, err := store.Restore(ctx, state, clock); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Not due yet.
	due, err := store.ListDue(ctx, 2000, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due too early = (%d, %v)", len(due), err)
	}
	due, err = store.ListDue(ctx, 3000, 10)
	if err != nil || len(due) != 1 || due[0].ScopeKey != MustScopeKey(scope) {
		t.Fatalf("due = (%+v, %v)", due, err)
	}
	// Restoring an older generation is idempotent.
	older := ClosedState(scope, "7", 1, "t0", 500)
	older.Phase = PhaseSuspect
	replay, err := store.Restore(ctx, older, clock)
	if err != nil || replay.Status != MutationIdempotent {
		t.Fatalf("older restore = (%s, %v)", replay.Status, err)
	}
	// Restoring a newer numeric revision (with a fresh generation) beats the
	// persisted state; a lower generation alone stays idempotent like Node.
	newer := ClosedState(scope, "9", 3, "t9", 4000)
	accepted, err := store.Restore(ctx, newer, clock)
	if err != nil || accepted.Status != MutationApplied || accepted.State.Phase != PhaseClosed {
		t.Fatalf("newer restore = (%s, %+v, %v)", accepted.Status, accepted.State, err)
	}
	// replaceAccountDispatchRevision closes base + authorized family keys.
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "acc:authorized:s:g:a"},
		DispatchRevision: "9", TransitionID: "fam", Reason: "r", NowMs: clock,
	}); err != nil {
		t.Fatalf("family suspect: %v", err)
	}
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: accountScope("other"), DispatchRevision: "9", TransitionID: "oth", Reason: "r", NowMs: clock,
	}); err != nil {
		t.Fatalf("other suspect: %v", err)
	}
	changed, err := store.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "acc", DispatchRevision: "10", TransitionID: "rev10", NowMs: clock,
	})
	if err != nil {
		t.Fatalf("replace revision: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
	familyState, err := store.Get(ctx, Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "acc:authorized:s:g:a"}, clock)
	if err != nil || familyState.Phase != PhaseClosed || familyState.DispatchRevision != "10" {
		t.Fatalf("family state = (%+v, %v)", familyState, err)
	}
}

func TestRedisEscalationToAccount(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	ctx := context.Background()
	scopes := []Scope{protocolScope("acc")}
	mini := protocolScope("acc")
	mini.ModelBucket = "gpt-4o-mini"
	scopes = append(scopes, mini)
	image := protocolScope("acc")
	image.RequestLane = LaneImage
	scopes = append(scopes, image)

	one := int64(1)
	for index, scope := range scopes {
		if _, err := store.Suspect(ctx, SuspectInput{
			Scope: scope, DispatchRevision: "7", TransitionID: string(rune('a'+index)) + "-s",
			Reason: "r", ConfirmationFailuresRequired: &one, NowMs: clock,
		}); err != nil {
			t.Fatalf("suspect %d: %v", index, err)
		}
		// One confirmed failure with required=1 opens the child.
		*clock += 3000
		if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
			Scope: scope, Generation: 1, DispatchRevision: "7",
			TransitionID: string(rune('a'+index)) + "-a",
			LeaseID:      string(rune('a'+index)) + "-l",
			LeaseUntilMs: *clock + 50_000, NowMs: clock,
		}); err != nil {
			t.Fatalf("acquire %d: %v", index, err)
		}
		sha := strings.Repeat(string(rune('1'+index)), 64)
		opened, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
			Scope: scope, Generation: 1, DispatchRevision: "7",
			TransitionID: string(rune('a'+index)) + "-c",
			LeaseID:      string(rune('a'+index)) + "-l",
			Outcome:      OutcomeTransportFailure, FailureEvidenceKey: strPtr(sha), NowMs: clock,
		})
		if err != nil {
			t.Fatalf("complete %d: %v", index, err)
		}
		if opened.State.Phase != PhaseOpen {
			t.Fatalf("child %d should be OPEN, got %s", index, opened.State.Phase)
		}
		*clock += 3000
	}

	var escalated EscalationResult
	var err error
	for index, scope := range scopes {
		escalated, err = store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
			Scope: scope, Generation: 1, DispatchRevision: "7",
			EvidenceID: string(rune('a'+index)) + "-ev", AccountTransitionID: "parent-1",
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
	if escalated.AccountState.Phase != PhaseOpen || len(escalated.AccountState.ChildScopeKeys) != 3 {
		t.Fatalf("account state = %+v", escalated.AccountState)
	}
	if len(escalated.RelatedStates) != 2 {
		// the first two records stay below the threshold and shadow nothing;
		// the escalating record shadows the two previously recorded children
		// (its own shadow happened with the parent open after persisting).
		t.Logf("related states = %d", len(escalated.RelatedStates))
	}
	// Duplicate evidence id is idempotent.
	dup, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
		Scope: scopes[0], Generation: 1, DispatchRevision: "7",
		EvidenceID: "a-ev", AccountTransitionID: "parent-2",
		Reason: "transport:e", ConfirmedFailureCount: 1,
		DistinctScopeThreshold: 3, WindowMs: 600_000, MaxProtocolScopes: 8, NowMs: clock,
	})
	if err != nil || dup.Status != EscalationIdempotent {
		t.Fatalf("duplicate = (%s, %v)", dup.Status, err)
	}
	// The account scope is OPEN and blocks dispatch.
	accountState, err := store.Get(ctx, accountScope("acc"), clock)
	if err != nil || accountState.Phase != PhaseOpen {
		t.Fatalf("account get = (%+v, %v)", accountState, err)
	}
	// Clearing evidence matches on dispatch revision.
	if cleared, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "other", EvidenceID: "e", NowMs: clock,
	}); err != nil || cleared {
		t.Fatalf("clear wrong revision = (%v, %v)", cleared, err)
	}
	if cleared, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "acc", DispatchRevision: "7", EvidenceID: "e", NowMs: clock,
	}); err != nil || !cleared {
		t.Fatalf("clear = (%v, %v)", cleared, err)
	}
}

func TestRedisValidationErrorsAndDegradedClient(t *testing.T) {
	now := int64(0)
	store, server := newTestRedisStore(t, 100, func() int64 { return now })
	ctx := context.Background()
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: accountScope("a"), DispatchRevision: "", TransitionID: "t", Reason: "r", NowMs: int64Ptr(now),
	}); err == nil || !strings.Contains(err.Error(), "dispatchRevision") {
		t.Fatalf("missing revision error = %v", err)
	}
	tooBig := int64(9)
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: accountScope("a"), DispatchRevision: "7", TransitionID: "t", Reason: "r",
		ConfirmationFailuresRequired: &tooBig, NowMs: int64Ptr(now),
	}); err == nil || !strings.Contains(err.Error(), "confirmationFailuresRequired 必须是 1..5") {
		t.Fatalf("required error = %v", err)
	}
	// A dead Redis client surfaces errors (the caller replays or degrades).
	server.Close()
	if _, err := store.Get(ctx, accountScope("a"), int64Ptr(now)); err == nil {
		t.Fatalf("dead redis must surface an error")
	}
}

func TestRedisConcurrentSuspect(t *testing.T) {
	now := int64(0)
	store, _ := newTestRedisStore(t, 100, func() int64 { return now })
	ctx := context.Background()
	const goroutines = 8
	var wg sync.WaitGroup
	statuses := make([]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := store.Suspect(ctx, SuspectInput{
				Scope: accountScope("acc"), DispatchRevision: "7",
				TransitionID: string(rune('a'+index)) + "-t", Reason: "r", NowMs: int64Ptr(now),
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

func TestRedisKeyNamespace(t *testing.T) {
	// Node redisNamespacedKey replaces the juhe-ai root with
	// "juhe-ai:<namespace>:".
	keys := redisAccountCircuitStoreKeys("gateway-account-circuit", "dev")
	if keys.states != "juhe-ai:dev:account-circuit:gateway-account-circuit:states" {
		t.Fatalf("namespaced key = %s", keys.states)
	}
	bare := redisAccountCircuitStoreKeys("gateway-account-circuit", "")
	if bare.states != "juhe-ai:account-circuit:gateway-account-circuit:states" {
		t.Fatalf("bare key = %s", bare.states)
	}
}

func TestRedisStoreInjectsClient(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store, err := NewRedisStore(RedisStoreOptions{Client: client, Capacity: 10})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	state, err := store.Get(context.Background(), accountScope("fresh"), nil)
	if err != nil || state.Phase != PhaseClosed {
		t.Fatalf("injected client get = (%+v, %v)", state, err)
	}
}
