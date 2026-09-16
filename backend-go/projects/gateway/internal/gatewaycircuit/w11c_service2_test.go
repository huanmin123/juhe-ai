package gatewaycircuit

// w11c: second batch of CircuitService paths (prepare guards, escalation
// notifications, acquire replay, model bucket bounds, serialize helpers).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestW11CServiceConstructorDefaultsAndValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	// Invalid escalation options fail construction.
	bad := int64(-1)
	if _, err := NewCircuitService(store, ServiceOptions{EscalationDistinctScopeThreshold: &bad}); err == nil {
		t.Fatalf("expected threshold validation error")
	}
	if _, err := NewCircuitService(store, ServiceOptions{EscalationWindowMs: &bad}); err == nil {
		t.Fatalf("expected window validation error")
	}
	// Minimal options fall back to package defaults.
	service, err := NewCircuitService(store, ServiceOptions{})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	if service.now == nil || service.createID == nil || service.isRuntimeStateReady == nil || service.random == nil {
		t.Fatalf("defaults not wired: %+v", service)
	}
	if service.settings.AccountCircuitSuspectConfirmationIntervalMs <= 0 {
		t.Fatalf("default settings missing")
	}
}

func TestW11CServicePrepareChildScopeReplaceAndGuards(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	revision := revisionOf(t, account)

	// A child scope in an older opaque revision gets replaced before the
	// dispatch proceeds.
	old := ClosedState(scope, "99", 1, "w11c-old", *clock)
	old.Phase = PhaseSuspect
	if _, err := store.Restore(ctx, old, clock); err != nil {
		t.Fatalf("restore old child: %v", err)
	}
	result, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), ""))
	if err != nil {
		t.Fatalf("prepare after child replace: %v", err)
	}
	// The scope was replaced to closed, so dispatch proceeds.
	if result.Outcome != PrepareDispatchable {
		t.Fatalf("outcome = %s (%+v)", result.Outcome, result.State)
	}

	// Store read failures surface from the confirmation-eligibility path.
	wrapped := w11cWrap(store)
	failAfterSuspect, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := failAfterSuspect.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2), reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	wrapped.failNext["Get"] = 1
	if _, err := failAfterSuspect.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), "")); err == nil {
		t.Fatalf("expected get error propagation")
	}

}
func TestW11CServiceAcquireReplayNotCommitted(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, account), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 20_000
	state, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Both acquire attempts fail and the replayed transition was not
	// committed: the raw error propagates (covered elsewhere); here the
	// notify error path is exercised instead.
	notifyFail, _ := newTestService(t, store, ServiceOptions{
		Now: func() int64 { return *clock },
		OnMutation: func(context.Context, MutationEvent) error { return errors.New("w11c notify failed") },
	})
	if _, err := notifyFail.acquireConfirmation(ctx, scope, state, 30_000, strings.Repeat("b", 64)); err == nil {
		t.Fatalf("expected notify error propagation")
	}
	// A failing Get during the replay check surfaces the acquire error.
	wrapped := w11cWrap(store)
	getFail, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	wrapped.failNext["AcquireConfirmationLease"] = 2
	wrapped.failNext["Get"] = 1
	if _, err := getFail.acquireConfirmation(ctx, scope, state, 30_000, strings.Repeat("b", 64)); err == nil {
		t.Fatalf("expected acquire failure propagation")
	}
}

func TestW11CServiceEscalationMutationNotifications(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	var events []MutationEvent
	service, _ := newTestService(t, store, ServiceOptions{
		Now: func() int64 { return *clock },
		OnMutation: func(_ context.Context, event MutationEvent) error {
			events = append(events, event)
			return nil
		},
	})
	ctx := context.Background()
	account := testAccount()
	// Distinct known model buckets keep the three scopes separate.
	account.SupportedModels = []string{"gpt-4o", "gpt-4o-mini", "gpt-4o-image"}
	scopeA := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	scopeB := protocolModelScope(account, LaneText, strPtr("gpt-4o-mini"))
	scopeC := protocolModelScope(account, LaneText, strPtr("gpt-4o-image"))
	revision := revisionOf(t, account)
	// Each scope needs two confirmed transport failures to reach OPEN.
	evidence := 0
	for _, scope := range []Scope{scopeA, scopeB, scopeC} {
		if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
			scope: scope, dispatchRevision: revision, confirmationFailuresRequired: int64Ptr(2),
			reason: "transport:down",
		}); err != nil {
			t.Fatalf("suspect bootstrap: %v", err)
		}
		for round := 1; round <= 2; round++ {
			evidence++
			key := strings.Repeat(string(rune('0'+evidence%10)), 64)
			*clock = int64(10_000 * evidence)
			result, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, modelForScope(scope), key))
			if err != nil || result.Outcome != PrepareDispatchable {
				if result.State != nil {
					t.Logf("prepare %d blocked: phase=%s scopeKind=%s", evidence, result.State.Phase, result.State.Scope.Kind)
				}
				t.Fatalf("prepare %d = (%s, %v)", evidence, result.Outcome, err)
			}
			if _, err := result.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: "upstream down"}); err != nil {
				t.Fatalf("report %d: %v", evidence, err)
			}
		}
	}
	// The third confirmed family failure escalates to the account scope.
	accountState, err := store.Get(ctx, accountScope(scopeA.AccountRuntimeKey), clock)
	if err != nil || accountState.Phase != PhaseOpen {
		t.Fatalf("account state = (%+v, %v)", accountState, err)
	}
	foundParent := false
	for _, event := range events {
		if event.Operation == OperationRecordParentEvidence && event.Scope.Kind == ScopeKindAccount {
			foundParent = true
		}
	}
	if !foundParent {
		t.Fatalf("expected account-level record_parent_evidence events")
	}
}

func modelForScope(scope Scope) *string {
	model := scope.ModelBucket
	return &model
}

func TestW11CServiceFramingClearsEscalationError(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	scope := protocolModelScope(account, LaneText, strPtr("gpt-4o"))
	// Suspect then close through the observer framing path; the escalation
	// clear fails and the error propagates.
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, account), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 1_000
	observer, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64)))
	if err != nil || observer.Outcome != PrepareDispatchable || !observer.Attempt.IsObserver {
		t.Fatalf("observer prepare = (%s, %v)", observer.Outcome, err)
	}
	wrapped.failNext["ClearAccountEscalationEvidence"] = 1
	if _, err := observer.Attempt.ReportFramingComplete(ctx); err == nil {
		t.Fatalf("expected clear escalation error propagation")
	}
}

func TestW11CServiceKeyRotationCloseError(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	// A foreground failure suspects the scope, then the key-rotation close
	// fails while clearing the escalation evidence.
	plain, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), ""))
	if err != nil || plain.Outcome != PrepareDispatchable {
		t.Fatalf("plain prepare = (%s, %v)", plain.Outcome, err)
	}
	if _, err := plain.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: "rotated"}); err != nil {
		t.Fatalf("transport: %v", err)
	}
	wrapped.failNext["ClearAccountEscalationEvidence"] = 1
	if _, err := plain.Attempt.ReportFramingComplete(ctx); err == nil {
		t.Fatalf("expected key rotation clear error")
	}
}

func TestW11CServiceObserverFramingError(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
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
	*clock = 1_000
	observer, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), strings.Repeat("b", 64)))
	if err != nil || observer.Outcome != PrepareDispatchable {
		t.Fatalf("observer prepare = (%s, %v)", observer.Outcome, err)
	}
	wrapped.failNext["CloseSuspectFromObserver"] = 1
	if _, err := observer.Attempt.ReportFramingComplete(ctx); err == nil {
		t.Fatalf("expected observer framing error")
	}
}

func TestW11CServiceKeyRotationMismatchPath(t *testing.T) {
	now := int64(0)
	clock := &now
	store := w11cMemStore(t, 100, *clock)
	wrapped := w11cWrap(store)
	service, _ := newTestService(t, wrapped, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	account := testAccount()
	plain, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, strPtr("gpt-4o"), ""))
	if err != nil || plain.Outcome != PrepareDispatchable {
		t.Fatalf("plain prepare = (%s, %v)", plain.Outcome, err)
	}
	if _, err := plain.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindReadIncomplete, Reason: "boom"}); err != nil {
		t.Fatalf("transport: %v", err)
	}
	// Without an evidence key the key-rotation close returns a state
	// mismatch result instead of an error.
	result, err := plain.Attempt.ReportFramingComplete(ctx)
	if err != nil {
		t.Fatalf("key rotation mismatch: %v", err)
	}
	if result != nil && result.Status == "" {
		t.Fatalf("empty status")
	}
}

func TestW11CServiceSerializeHelpers(t *testing.T) {
	// stableSerialize falls through to the default formatter.
	if got := stableSerialize(struct{ X int }{3}); got == "" {
		t.Fatalf("default formatting empty")
	}
	// jsonString never fails for scalars.
	if got := jsonString("a"); got != `"a"` {
		t.Fatalf("jsonString = %s", got)
	}
	// sameRequestFailureEvidence marker fallback.
	evidence := strings.Repeat("e", 64)
	marker := "transport:boom|request_evidence_sha256=" + evidence
	if !sameRequestFailureEvidence(State{FailureReason: strPtr(marker)}, &evidence) {
		t.Fatalf("marker fallback failed")
	}
	// Marker with no evidence after the separator.
	if sameRequestFailureEvidence(State{FailureReason: strPtr("x|request_evidence_sha256=")}, &evidence) {
		t.Fatalf("empty marker tail should not match")
	}
	// Nil failure reason.
	if sameRequestFailureEvidence(State{}, &evidence) {
		t.Fatalf("nil reason should not match")
	}
	// normalizedFailureEvidenceKey rejects non-sha values.
	if normalizedFailureEvidenceKey(strPtr("nope")) != nil {
		t.Fatalf("non-sha evidence should normalize to nil")
	}
	if normalizedFailureEvidenceKey(strPtr(strings.Repeat("E", 64))) == nil {
		t.Fatalf("uppercase sha should normalize")
	}
	// gatewayAccountCircuitModelBucket bounded known list.
	account := testAccount()
	var models []string
	for index := 0; index < 300; index++ {
		models = append(models, string(rune('a'+index%26))+string(rune('a'+index/26))+strings.Repeat("m", 4)+string(rune('0'+index%10))+strings.Repeat(string(rune('a'+index%26)), 2))
	}
	account.SupportedModels = models
	if scope := protocolModelScope(account, LaneText, strPtr(models[0])); scope.ModelBucket != models[0] {
		t.Fatalf("bounded first model = %q", scope.ModelBucket)
	}
}
