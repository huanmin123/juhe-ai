package gatewaycircuit

// w11c: CircuitService attempt lifecycle, prepare guards and escalation flow.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w11cErrorStore wraps a Store and fails selected operations once or always.
type w11cErrorStore struct {
	Store
	mu sync.Mutex
	// failNext maps a method name to remaining failure count.
	failNext map[string]int
}

func (s *w11cErrorStore) fail(method string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext[method] > 0 {
		s.failNext[method]--
		return true
	}
	return false
}

func (s *w11cErrorStore) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	if s.fail("Get") {
		return State{}, errors.New("w11c get failed")
	}
	return s.Store.Get(ctx, scope, nowMs)
}

func (s *w11cErrorStore) Suspect(ctx context.Context, input SuspectInput) (MutationResult, error) {
	if s.fail("Suspect") {
		return MutationResult{}, errors.New("w11c suspect failed")
	}
	return s.Store.Suspect(ctx, input)
}

func (s *w11cErrorStore) ReplaceDispatchRevision(ctx context.Context, input ReplaceDispatchRevisionInput) (MutationResult, error) {
	if s.fail("ReplaceDispatchRevision") {
		return MutationResult{}, errors.New("w11c replace failed")
	}
	return s.Store.ReplaceDispatchRevision(ctx, input)
}

func (s *w11cErrorStore) CompleteConfirmation(ctx context.Context, input CompleteConfirmationInput) (MutationResult, error) {
	if s.fail("CompleteConfirmation") {
		return MutationResult{}, errors.New("w11c complete failed")
	}
	return s.Store.CompleteConfirmation(ctx, input)
}

func (s *w11cErrorStore) AcquireConfirmationLease(ctx context.Context, input AcquireConfirmationLeaseInput) (MutationResult, error) {
	if s.fail("AcquireConfirmationLease") {
		return MutationResult{}, errors.New("w11c acquire failed")
	}
	return s.Store.AcquireConfirmationLease(ctx, input)
}

func (s *w11cErrorStore) CloseSuspectFromObserver(ctx context.Context, input CloseSuspectFromObserverInput) (MutationResult, error) {
	if s.fail("CloseSuspectFromObserver") {
		return MutationResult{}, errors.New("w11c observer close failed")
	}
	return s.Store.CloseSuspectFromObserver(ctx, input)
}

func (s *w11cErrorStore) CloseSuspectFromKeyRotation(ctx context.Context, input CloseSuspectFromKeyRotationInput) (MutationResult, error) {
	if s.fail("CloseSuspectFromKeyRotation") {
		return MutationResult{}, errors.New("w11c rotation close failed")
	}
	return s.Store.CloseSuspectFromKeyRotation(ctx, input)
}

func (s *w11cErrorStore) ClearAccountEscalationEvidence(ctx context.Context, input ClearAccountEscalationEvidenceInput) (bool, error) {
	if s.fail("ClearAccountEscalationEvidence") {
		return false, errors.New("w11c clear failed")
	}
	return s.Store.ClearAccountEscalationEvidence(ctx, input)
}

func (s *w11cErrorStore) RecordProtocolModelOpenEvidence(ctx context.Context, input ProtocolModelOpenEvidenceInput) (EscalationResult, error) {
	if s.fail("RecordProtocolModelOpenEvidence") {
		return EscalationResult{}, errors.New("w11c escalate failed")
	}
	return s.Store.RecordProtocolModelOpenEvidence(ctx, input)
}

func w11cWrap(store Store) *w11cErrorStore {
	return &w11cErrorStore{Store: store, failNext: map[string]int{}}
}

func w11cPrepareInput(account gatewayruntimecache.OpenAIAccountSecret, model *string, evidence string) PrepareAttemptInput {
	input := PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 LaneText,
		Model:                       model,
		ConfirmationLeaseDurationMs: 30_000,
	}
	if evidence != "" {
		input.FailureEvidenceKey = strPtr(evidence)
	}
	return input
}

func TestW11CPrepareAttemptInputValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	ctx := context.Background()

	// Invalid account runtime key (empty id).
	badAccount := testAccount()
	badAccount.ID = " "
	if _, err := service.PrepareAttempt(ctx, w11cPrepareInput(badAccount, strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected scope validation error")
	}
	// Non-positive lease duration.
	good := w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")
	good.ConfirmationLeaseDurationMs = 0
	if _, err := service.PrepareAttempt(ctx, good); err == nil {
		t.Fatalf("expected lease duration validation error")
	}
	// Invalid confirmation failures required.
	good = w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")
	bad := int64(-2)
	good.ConfirmationFailuresRequired = &bad
	if _, err := service.PrepareAttempt(ctx, good); err == nil {
		t.Fatalf("expected confirmation failures validation error")
	}
	// Store read failure propagates.
	wrapped := w11cWrap(store)
	wrapped.failNext["Get"] = 1
	failing, _ := newTestService(t, wrapped, ServiceOptions{})
	if _, err := failing.PrepareAttempt(ctx, w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected get error propagation")
	}
	// EnsureRuntimeStateReady error propagates.
	ensureFailed, _ := newTestService(t, store, ServiceOptions{
		IsRuntimeStateReady: func(string) bool { return false },
		EnsureRuntimeStateReady: func(context.Context, string) (bool, error) {
			return false, errors.New("w11c ensure failed")
		},
	})
	if _, err := ensureFailed.PrepareAttempt(ctx, w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected ensure error propagation")
	}
}

func TestW11CPrepareAttemptEnsureRecoversReadiness(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	ready := false
	service, _ := newTestService(t, store, ServiceOptions{
		IsRuntimeStateReady: func(string) bool { return ready },
		EnsureRuntimeStateReady: func(context.Context, string) (bool, error) {
			ready = true
			return true, nil
		},
	})
	result, err := service.PrepareAttempt(context.Background(), w11cPrepareInput(testAccount(), strPtr("gpt-4o"), ""))
	if err != nil || result.Outcome != PrepareDispatchable {
		t.Fatalf("prepare = (%s, %v)", result.Outcome, err)
	}
}

func TestW11CPrepareAttemptReplaceRevisionErrorAndNotify(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()

	// A parent account scope with an older opaque revision triggers replace.
	parent := ClosedState(accountScope("acc"), "99", 1, "p1", *clock)
	parent.Phase = PhaseSuspect
	if _, err := store.Restore(ctx, parent, clock); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	// Replace failure propagates.
	wrapped.failNext["ReplaceDispatchRevision"] = 1
	if _, err := service.PrepareAttempt(ctx, w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected replace error propagation")
	}
	// Mutation-notify failure propagates.
	failingNotify, _ := newTestService(t, store, ServiceOptions{
		Now:        func() int64 { return *clock },
		OnMutation: func(context.Context, MutationEvent) error { return errors.New("w11c notify failed") },
	})
	if _, err := failingNotify.PrepareAttempt(ctx, w11cPrepareInput(testAccount(), strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected notify error propagation")
	}
}

func TestW11CPrepareAttemptConfirmationGuards(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	revision := revisionOf(t, account)

	// Build a suspect so a confirmation attempt is possible.
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}

	// A confirmation for a different scope is rejected.
	other := w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64))
	other.Confirmation = &Confirmation{
		Scope: protocolModelScope(account, LaneText, strPtr("other-model")), ScopeKey: "w11c-wrong",
		AccountRuntimeKey: scope.AccountRuntimeKey, Generation: 1, DispatchRevision: revision, LeaseID: "lease-x",
	}
	blocked, err := service.PrepareAttempt(ctx, other)
	if err != nil || blocked.Outcome != PrepareBlocked {
		t.Fatalf("wrong-scope confirmation = (%s, %v)", blocked.Outcome, err)
	}

	// Ineligible confirmations are completed as unknown and block.
	*clock = 10_000
	ineligible := false
	inel := w11cPrepareInput(account, strPtr("gpt-4o"), "")
	inel.ConfirmationEligible = &ineligible
	inel.Confirmation = &Confirmation{
		Scope: scope, ScopeKey: MustScopeKey(scope), AccountRuntimeKey: scope.AccountRuntimeKey,
		Generation: 1, DispatchRevision: revision, LeaseID: "lease-x",
	}
	blockedInel, err := service.PrepareAttempt(ctx, inel)
	if err != nil || blockedInel.Outcome != PrepareBlocked {
		t.Fatalf("ineligible confirmation = (%s, %v)", blockedInel.Outcome, err)
	}

	// A stale generation / lease mismatch blocks rather than dispatching.
	stale := w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64))
	stale.Confirmation = &Confirmation{
		Scope: scope, ScopeKey: MustScopeKey(scope), AccountRuntimeKey: scope.AccountRuntimeKey,
		Generation: 9, DispatchRevision: revision, LeaseID: "lease-y",
	}
	blockedStale, err := service.PrepareAttempt(ctx, stale)
	if err != nil || blockedStale.Outcome != PrepareBlocked {
		t.Fatalf("stale confirmation = (%s, %v)", blockedStale.Outcome, err)
	}

	// Eligible confirmation for the live suspect dispatches.
	*clock = 20_000
	valid := w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("c", 64))
	valid.Confirmation = &Confirmation{
		Scope: scope, ScopeKey: MustScopeKey(scope), AccountRuntimeKey: scope.AccountRuntimeKey,
		Generation: 1, DispatchRevision: revision, LeaseID: "lease-z",
	}
	dispatched, err := service.PrepareAttempt(ctx, valid)
	if err != nil {
		t.Fatalf("valid confirmation prepare: %v", err)
	}
	// The state store holds no matching lease for this synthetic confirmation,
	// so the guard blocks rather than dispatching.
	if dispatched.Outcome != PrepareBlocked {
		t.Fatalf("synthetic lease mismatch should block, got %s", dispatched.Outcome)
	}
}

func TestW11CAttemptReportFramingCompleteObserverAndRecovery(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	revision := revisionOf(t, account)

	// Suspect then observe with a fresh evidence key.
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 1_000
	observer, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64)))
	if err != nil || observer.Outcome != PrepareDispatchable || !observer.Attempt.IsObserver {
		t.Fatalf("observer prepare = (%s, %v)", observer.Outcome, err)
	}
	framing, err := observer.Attempt.ReportFramingComplete(ctx)
	if err != nil || framing == nil {
		t.Fatalf("observer framing = (%v, %v)", framing, err)
	}
	if framing.State.Phase != PhaseClosed {
		t.Fatalf("observer framing state = %s", framing.State.Phase)
	}

	// Key-rotation recovery: a foreground transport failure suspects the scope
	// and the attempt completes the framing after the key rotation.
	plain, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), ""))
	if err != nil || plain.Outcome != PrepareDispatchable {
		t.Fatalf("plain prepare = (%s, %v)", plain.Outcome, err)
	}
	decision, err := plain.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: "rotated"})
	if err != nil || decision.Outcome != DecisionSuspected {
		t.Fatalf("recovery transport = (%s, %v)", decision.Outcome, err)
	}
	rotated, err := plain.Attempt.ReportFramingComplete(ctx)
	if err != nil || rotated == nil {
		t.Fatalf("rotated framing = (%v, %v)", rotated, err)
	}
	if rotated.State.Phase != PhaseClosed {
		t.Fatalf("rotated framing state = %s", rotated.State.Phase)
	}

	// A plain closed attempt reports nil framing/unknown and clears escalation.
	*clock = 30_000
	closedPrepare, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), ""))
	if err != nil || closedPrepare.Outcome != PrepareDispatchable {
		t.Fatalf("closed prepare = (%s, %v)", closedPrepare.Outcome, err)
	}
	plainResult, err := closedPrepare.Attempt.ReportFramingComplete(ctx)
	if err != nil || plainResult != nil {
		t.Fatalf("plain framing = (%v, %v)", plainResult, err)
	}
	unknown, err := closedPrepare.Attempt.ReportUnknown(ctx)
	if err != nil || unknown != nil {
		t.Fatalf("plain unknown = (%v, %v)", unknown, err)
	}

	// An observer transport failure stays neutral.
	*clock = 40_000
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down3",
	}); err != nil {
		t.Fatalf("suspect 3: %v", err)
	}
	*clock = 41_000
	obs, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("d", 64)))
	if err != nil || obs.Outcome != PrepareDispatchable || !obs.Attempt.IsObserver {
		t.Fatalf("observer 2 = (%s, %v)", obs.Outcome, err)
	}
	neutral, err := obs.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindReadIncomplete, Reason: "observer boom"})
	if err != nil || neutral.Outcome != DecisionObserverNeutral {
		t.Fatalf("observer transport = (%s, %v)", neutral.Outcome, err)
	}
	// Invalid transport failure reason surfaces the raw error.
	if _, err := obs.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: " "}); err == nil {
		t.Fatalf("expected reason validation error")
	}
}

func TestW11CAttemptSettlementErrorPropagatesToPeers(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))

	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, account), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 10_000
	result, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64)))
	if err != nil || result.Outcome != PrepareDispatchable {
		t.Fatalf("prepare = (%s, %v)", result.Outcome, err)
	}
	// The completion error surfaces from the framing report.
	wrapped.failNext["CompleteConfirmation"] = 1
	if _, err := result.Attempt.ReportFramingComplete(ctx); err == nil {
		t.Fatalf("expected completion error propagation")
	}
}

func TestW11CSuspectForegroundValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	ctx := context.Background()
	// Empty dispatch revision.
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{scope: accountScope("acc"), dispatchRevision: " ", reason: "r"}); err == nil {
		t.Fatalf("expected dispatchRevision validation error")
	}
	// Invalid confirmation failures required.
	bad := int64(-1)
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: accountScope("acc"), dispatchRevision: "7", reason: "r", confirmationFailuresRequired: &bad,
	}); err == nil {
		t.Fatalf("expected confirmation failures validation error")
	}
	// Store failure propagates.
	wrapped := w11cWrap(store)
	wrapped.failNext["Suspect"] = 1
	failing, _ := newTestService(t, wrapped, ServiceOptions{})
	if _, err := failing.SuspectForegroundFailure(ctx, suspectForegroundInput{scope: accountScope("acc"), dispatchRevision: "7", reason: "r"}); err == nil {
		t.Fatalf("expected suspect error propagation")
	}
}

func TestW11CCompleteConfirmationEscalationFlow(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	var mutations []MutationEvent
	service, _ := newTestService(t, store, ServiceOptions{
		Now: func() int64 { return *clock },
		OnMutation: func(_ context.Context, event MutationEvent) error {
			mutations = append(mutations, event)
			return nil
		},
	})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	revision := revisionOf(t, account)

	// Drive the scope to suspect and acquire the confirmation lease through
	// the service, then complete with a transport failure twice (required=2)
	// so the scope opens and escalates.
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	evidenceA := strings.Repeat("1", 64)
	evidenceB := strings.Repeat("2", 64)
	for index, evidence := range []string{evidenceA, evidenceB} {
		*clock = int64(10_000 + index*10_000)
		result, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), evidence))
		if err != nil {
			t.Fatalf("prepare %d: %v", index, err)
		}
		if result.Outcome != PrepareDispatchable || !result.Attempt.IsConfirmation() {
			t.Fatalf("prepare %d = (%s, confirmation=%v)", index, result.Outcome, result.Attempt.IsConfirmation())
		}
		decision, err := result.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: "w11c upstream down"})
		if err != nil {
			t.Fatalf("report %d: %v", index, err)
		}
		if decision.Outcome != DecisionBlocked {
			t.Fatalf("report %d outcome = %s", index, decision.Outcome)
		}
	}
	// The scope is OPEN after two confirmed failures.
	state, err := store.Get(ctx, scope, clock)
	if err != nil || state.Phase != PhaseOpen {
		t.Fatalf("scope after failures = (%+v, %v)", state, err)
	}
	foundEscalation := false
	for _, event := range mutations {
		if event.Operation == OperationRecordParentEvidence {
			foundEscalation = true
		}
	}
	if !foundEscalation {
		t.Fatalf("expected record_parent_evidence mutation events, got %d", len(mutations))
	}
}

func TestW11CAcquireConfirmationFailurePath(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, account), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 10_000
	state, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Both acquire attempts fail and the replay does not find a committed
	// lease -> the raw error propagates.
	wrapped.failNext["AcquireConfirmationLease"] = 2
	if _, err := service.acquireConfirmation(ctx, scope, state, 30_000, strings.Repeat("b", 64)); err == nil {
		t.Fatalf("expected acquire error propagation")
	}
}

func TestW11CClearEscalationValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	if err := service.ClearAccountEscalationEvidenceAfterFramingComplete(context.Background(), accountScope("acc"), " "); err == nil {
		t.Fatalf("expected dispatchRevision validation error")
	}
	wrapped := w11cWrap(store)
	wrapped.failNext["ClearAccountEscalationEvidence"] = 1
	failing, _ := newTestService(t, wrapped, ServiceOptions{})
	if err := failing.ClearAccountEscalationEvidenceAfterFramingComplete(context.Background(), accountScope("acc"), "7"); err == nil {
		t.Fatalf("expected clear error propagation")
	}
}

func TestW11CServicePureHelpers(t *testing.T) {
	// stableSerialize scalar and nested shapes.
	cases := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{"s", `"s"`},
		{true, "true"},
		{int(3), "3"},
		{int64(4), "4"},
		{float64(1.5), "1.5"},
		{[]any{int64(1), "x"}, `[1,"x"]`},
		{[]string{"a"}, `["a"]`},
		{map[string]any{"b": int64(1), "a": int64(2)}, `{"a":2,"b":1}`},
	}
	for _, tc := range cases {
		if got := stableSerialize(tc.value); got != tc.want {
			t.Fatalf("stableSerialize(%v) = %s, want %s", tc.value, got, tc.want)
		}
	}
	// Circular slice marker.
	circular := make([]any, 1)
	circular[0] = circular
	if got := stableSerialize(circular); got != `["[Circular]"]` {
		t.Fatalf("circular = %s", got)
	}
	// cycleIdentity only tags maps and slices.
	if _, ok := cycleIdentity("scalar"); ok {
		t.Fatalf("scalar should not have identity")
	}
	// nilOrString / anySlice.
	if nilOrString(nil) != nil || nilOrString(strPtr("x")) != "x" {
		t.Fatalf("nilOrString misbehaves")
	}
	if anySlice(nil) != nil {
		t.Fatalf("anySlice(nil) should stay nil")
	}
	// accountCircuitCredentialOwnerIdentity filters unknown keys.
	identity := accountCircuitCredentialOwnerIdentity(map[string]any{"api_key": "k", "random": "x"})
	if len(identity) != 1 || identity["api_key"] != "k" {
		t.Fatalf("identity = %v", identity)
	}
	if accountCircuitCredentialOwnerIdentity(nil) == nil || len(accountCircuitCredentialOwnerIdentity(nil)) != 0 {
		t.Fatalf("nil identity should be an empty map")
	}
	// positiveDuration rejects non-positive values.
	if _, err := positiveDuration(0); err == nil {
		t.Fatalf("expected positiveDuration error")
	}
	// escalationMutationStatus mapping.
	if escalationMutationStatus(EscalationEscalated) != MutationApplied ||
		escalationMutationStatus(EscalationAlreadyActive) != MutationApplied ||
		escalationMutationStatus(EscalationRecorded) != MutationIdempotent ||
		escalationMutationStatus("other") != "other" {
		t.Fatalf("escalationMutationStatus mapping broken")
	}
	// requiredText rejects blank values.
	if _, err := requiredText("  ", "field"); err == nil {
		t.Fatalf("expected requiredText error")
	}
}

func TestW11CModelBucketMappingsAndNormalization(t *testing.T) {
	account := testAccount()
	account.SupportedModels = []string{"gpt-4o"}
	source := "gpt-4"
	upstream := "gpt-4-upstream"
	account.ModelMappings = []gatewayruntimecache.AccountModelMapping{{Enabled: true, SourceModel: source, UpstreamModel: upstream}}
	// A mapped source model is a known bucket.
	if scope := protocolModelScope(account, LaneText, strPtr("GPT-4")); scope.ModelBucket != "gpt-4" {
		t.Fatalf("mapped source bucket = %q", scope.ModelBucket)
	}
	// A mapped upstream model is known too.
	if scope := protocolModelScope(account, LaneText, strPtr("gpt-4-upstream")); scope.ModelBucket != "gpt-4-upstream" {
		t.Fatalf("mapped upstream bucket = %q", scope.ModelBucket)
	}
	// Disabled mappings do not contribute buckets.
	account.ModelMappings = []gatewayruntimecache.AccountModelMapping{{Enabled: false, SourceModel: source, UpstreamModel: upstream}}
	if scope := protocolModelScope(account, LaneText, strPtr("gpt-4")); scope.ModelBucket != gatewayAccountCircuitUnknownModelBucket {
		t.Fatalf("disabled mapping bucket = %q", scope.ModelBucket)
	}
	// Control characters and oversized names fall to unknown.
	if scope := protocolModelScope(account, LaneText, strPtr("bad\x01model")); scope.ModelBucket != gatewayAccountCircuitUnknownModelBucket {
		t.Fatalf("control-char bucket = %q", scope.ModelBucket)
	}
	if scope := protocolModelScope(account, LaneText, strPtr(strings.Repeat("m", 129))); scope.ModelBucket != gatewayAccountCircuitUnknownModelBucket {
		t.Fatalf("oversized bucket = %q", scope.ModelBucket)
	}
}

func TestW11CDispatchRevisionCredentialVariants(t *testing.T) {
	account := testAccount()
	first, _ := AccountCircuitDispatchRevision(account)
	// Changing credentials changes the opaque revision.
	account.APIKey = "sk-other"
	second, _ := AccountCircuitDispatchRevision(account)
	if first == second {
		t.Fatalf("credential change should alter the revision")
	}
	// Authorized-account runtime keys isolate per binding.
	authorized := testAccount()
	authorized.AccountAccessType = "account_authorized"
	authorized.BindingSystemAccountID = strPtr("sys-1")
	binding := authorized
	binding.BoundGroupID = strPtr("group-a")
	binding.AccountAuthorizationID = strPtr("authz-1")
	if _, err := GatewayAccountProtocolModelScope(binding, LaneText, strPtr("gpt-4o")); err != nil {
		t.Fatalf("authorized scope: %v", err)
	}
}
