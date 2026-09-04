package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newTestService(t *testing.T, store Store, mutate ServiceOptions) (*CircuitService, *[]RoutingObservabilityEvent) {
	t.Helper()
	if mutate.Now == nil {
		mutate.Now = func() int64 { return 0 }
	}
	// A counter keeps transition/lease ids unique per service.
	var idSequence int64
	if mutate.CreateID == nil {
		mutate.CreateID = func() string {
			idSequence++
			return fmt.Sprintf("id-%d", idSequence)
		}
	}
	if len(mutate.Settings.AccountCircuitBackoffMs) == 0 {
		mutate.Settings = DefaultSettings()
	}
	if mutate.Random == nil {
		mutate.Random = func() float64 { return 0.5 }
	}
	events := &[]RoutingObservabilityEvent{}
	service, err := NewCircuitService(store, mutate)
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	service.SetObservabilitySink(func(event RoutingObservabilityEvent) {
		*events = append(*events, event)
	})
	return service, events
}

func testAccount() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "openai_profile",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Type:                      "api_key",
		APIKey:                    "sk-test",
	}
}

func TestPrepareAttemptDispatchableWhenClosed(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText,
		Model: strPtr("gpt-4o"), ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != PrepareDispatchable || result.Attempt == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Attempt.IsConfirmation() {
		t.Fatalf("plain attempt must not carry a confirmation")
	}
}

func newNonExpiringMemoryStore(t *testing.T) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{
		Capacity: 100, Now: func() int64 { return 0 },
		Random: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

func TestPrepareAttemptBlockedWhenRuntimeStateRebuilding(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	ready := false
	service, events := newTestService(t, store, ServiceOptions{
		IsRuntimeStateReady: func(string) bool { return ready },
	})
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText,
		Model: strPtr("gpt-4o"), ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != PrepareBlocked || result.State == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.State.Phase != PhaseSuspect || result.State.FailureReason == nil || *result.State.FailureReason != "runtime_state_rebuilding" {
		t.Fatalf("blocked state = %+v", result.State)
	}
	found := false
	for _, event := range *events {
		if event.Kind == "circuit_dispatch" && event.Outcome == "rebuild_blocked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rebuild_blocked event missing: %v", *events)
	}
}

func TestPrepareAttemptConfirmationLeaseFlow(t *testing.T) {
	now := int64(0)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})

	// Create a SUSPECT incident from an earlier foreground failure.
	decision, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope: GatewayAccountProtocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:           "transport:connect failed",
		failureEvidenceKey: strPtr(strings.Repeat("a", 64)),
	})
	if err != nil || decision.Outcome != DecisionSuspected {
		t.Fatalf("suspect = (%s, %v)", decision.Outcome, err)
	}
	// Before retryAt the caller becomes an observer attempt (dispatchable
	// without a confirmation lease) as long as its evidence key is fresh.
	*clock = 1000
	blocked, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if blocked.Outcome != PrepareDispatchable || blocked.Attempt == nil || !blocked.Attempt.IsObserver {
		t.Fatalf("observer result = %+v", blocked)
	}
	// After retryAt the caller can acquire a confirmation lease.
	*clock = 3000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil {
		t.Fatalf("PrepareAttempt 2: %v", err)
	}
	if result.Outcome != PrepareDispatchable || result.Attempt == nil || !result.Attempt.IsConfirmation() {
		t.Fatalf("result = %+v", result)
	}
}

func revisionOf(t *testing.T, account gatewayruntimecache.OpenAIAccountSecret) string {
	t.Helper()
	revision, err := AccountCircuitDispatchRevision(account)
	if err != nil {
		t.Fatalf("dispatch revision: %v", err)
	}
	return revision
}

func TestReportTransportFailureSuspectThenOpen(t *testing.T) {
	now := int64(0)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	scope := GatewayAccountProtocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))

	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil || result.Outcome != PrepareDispatchable {
		t.Fatalf("prepare = (%s, %v)", result.Outcome, err)
	}
	decision, err := result.Attempt.ReportTransportFailure(context.Background(), TransportFailure{
		Kind: TransportFailureKindTransport, Reason: "connect refused",
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if decision.Outcome != DecisionSuspected || decision.State.Phase != PhaseSuspect {
		t.Fatalf("decision = (%s, %+v)", decision.Outcome, decision.State)
	}
	// The failure reason carries the evidence marker only when the request
	// carried an evidence key.
	if strings.Contains(*decision.State.FailureReason, "|request_evidence_sha256=") {
		t.Fatalf("failureReason should stay bare without evidence: %s", *decision.State.FailureReason)
	}
	// A second foreground failure with fresh evidence is still just a suspect
	// (per-request path); opening requires confirmed failures.
	decision2, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:again",
		failureEvidenceKey:           strPtr(strings.Repeat("c", 64)),
	})
	if err != nil || decision2.Outcome != DecisionBlocked {
		// The second suspect hits state_mismatch (already SUSPECT) -> blocked.
		t.Fatalf("second suspect = (%s, %v)", decision2.Outcome, err)
	}
}

func TestConfirmationSettlementDeduplicates(t *testing.T) {
	now := int64(0)
	clock := &now
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	completions := 0
	wrapped := completionCountingStore{Store: store, onCompletion: func() { completions++ }}
	service, _ := newTestService(t, &wrapped, ServiceOptions{Now: func() int64 { return *clock }})

	// Build a confirmation through prepareAttempt.
	_, err = service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope: GatewayAccountProtocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:connect failed",
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || result.Outcome != PrepareDispatchable {
		t.Fatalf("prepare = (%s, %v)", result.Outcome, err)
	}
	attempt := result.Attempt

	// First terminal outcome owns the lease: framing wins, the later transport
	// failure reports the same settlement.
	framing, err := attempt.ReportFramingComplete(context.Background())
	if err != nil || framing == nil {
		t.Fatalf("framing = (%v, %v)", framing, err)
	}
	transport, err := attempt.ReportTransportFailure(context.Background(), TransportFailure{
		Kind: TransportFailureKindTimeout, Reason: "late failure",
	})
	if err != nil {
		t.Fatalf("transport after framing: %v", err)
	}
	// Node maps a settlement whose first outcome was framing_complete to
	// observer_neutral, carrying the settled (RECOVERING) state.
	if transport.Outcome != DecisionObserverNeutral || transport.State.Phase != PhaseRecovering {
		t.Fatalf("post-framing settlement = (%s, %+v)", transport.Outcome, transport.State)
	}
	if completions != 1 {
		t.Fatalf("completions = %d, want exactly 1", completions)
	}
}

type completionCountingStore struct {
	Store
	onCompletion func()
}

func (c *completionCountingStore) CompleteConfirmation(ctx context.Context, input CompleteConfirmationInput) (MutationResult, error) {
	c.onCompletion()
	return c.Store.CompleteConfirmation(ctx, input)
}

func TestPrepareAttemptParentAccountBlocksChild(t *testing.T) {
	now := int64(0)
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return now }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return now }})
	// Open the parent account scope.
	parent := ClosedState(accountScope("acc"), "7", 1, "p1", now)
	parent.Phase = PhaseOpen
	parent.Generation = 1
	parent.DispatchRevision = revisionOf(t, testAccount())
	if _, err := store.Restore(context.Background(), parent, int64Ptr(now)); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != PrepareBlocked || result.State == nil || result.State.Scope.Kind != ScopeKindAccount {
		t.Fatalf("result = %+v", result)
	}
}

func TestPrepareAttemptParentRevisionMismatchBlocks(t *testing.T) {
	now := int64(0)
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return now }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return now }})
	// A parent holding a numerically newer revision makes the incoming
	// revision stale and blocks dispatch.
	account := testAccount()
	numeric := int64(5)
	account.DispatchRevision = &numeric
	parent := ClosedState(accountScope("acc"), "9", 1, "p1", now)
	parent.Phase = PhaseSuspect
	if _, err := store.Restore(context.Background(), parent, int64Ptr(now)); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != PrepareBlocked {
		t.Fatalf("outcome = %s (%+v)", result.Outcome, result.State)
	}

	// A parent in an older non-numeric revision is replaced and dispatch
	// proceeds (Node replaces the parent's revision first).
	opaqueAccount := testAccount()
	otherParent := ClosedState(accountScope("acc"), "99", 1, "p2", now)
	otherParent.Phase = PhaseSuspect
	if _, err := store.Restore(context.Background(), otherParent, int64Ptr(now)); err != nil {
		t.Fatalf("restore parent 2: %v", err)
	}
	result, err = service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: opaqueAccount, RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt 2: %v", err)
	}
	if result.Outcome != PrepareDispatchable {
		t.Fatalf("outcome 2 = %s (%+v)", result.Outcome, result.State)
	}
	parentState, err := store.Get(context.Background(), accountScope("acc"), int64Ptr(now))
	if err != nil || parentState.Phase != PhaseClosed || parentState.DispatchRevision != revisionOf(t, opaqueAccount) {
		t.Fatalf("parent after replace = (%+v, %v)", parentState, err)
	}
}

type failingOnceStore struct {
	Store
	mu       sync.Mutex
	failNext bool
}

func (s *failingOnceStore) AcquireConfirmationLease(ctx context.Context, input AcquireConfirmationLeaseInput) (MutationResult, error) {
	s.mu.Lock()
	shouldFail := s.failNext
	s.failNext = false
	s.mu.Unlock()
	result, err := s.Store.AcquireConfirmationLease(ctx, input)
	if shouldFail && err == nil {
		return MutationResult{}, errors.New("redis reply lost")
	}
	return result, err
}

func TestAcquireConfirmationReplaysLostReply(t *testing.T) {
	now := int64(0)
	clock := &now
	memory, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	inner := &failingOnceStore{Store: memory}
	service, _ := newTestService(t, inner, ServiceOptions{Now: func() int64 { return *clock }})
	scope := GatewayAccountProtocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	_, err = service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2), reason: "transport:connect failed",
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	inner.mu.Lock()
	inner.failNext = true
	inner.mu.Unlock()
	state, err := memory.Get(context.Background(), scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	decision, err := service.acquireConfirmation(context.Background(), scope, state, 30_000, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("acquire with lost reply: %v", err)
	}
	if decision.Outcome != DecisionConfirmationAcquired || decision.Confirmation == nil {
		t.Fatalf("decision = (%s, %v)", decision.Outcome, decision.Confirmation)
	}
}

func TestModelBucketNormalization(t *testing.T) {
	account := testAccount()
	account.SupportedModels = []string{"GPT-4o", "  gpt-4o-mini  "}
	long := strings.Repeat("x", 200)
	tests := []struct {
		model string
		want  string
	}{
		{"GPT-4o", "gpt-4o"},
		{"gpt-4o", "gpt-4o"},
		{"gpt-4o-mini", "gpt-4o-mini"},
		{"unknown-model", gatewayAccountCircuitUnknownModelBucket},
		{long, gatewayAccountCircuitUnknownModelBucket},
		{"", gatewayAccountCircuitUnknownModelBucket},
	}
	for _, tt := range tests {
		scope := GatewayAccountProtocolModelScope(account, LaneText, strPtr(tt.model))
		if scope.ModelBucket != tt.want {
			t.Fatalf("model %q bucket = %q, want %q", tt.model, scope.ModelBucket, tt.want)
		}
	}
	// nil model falls back to the unknown bucket.
	if scope := GatewayAccountProtocolModelScope(account, LaneText, nil); scope.ModelBucket != gatewayAccountCircuitUnknownModelBucket {
		t.Fatalf("nil model bucket = %q", scope.ModelBucket)
	}
}

func TestAccountCircuitDispatchRevisionStable(t *testing.T) {
	account := testAccount()
	first, err := AccountCircuitDispatchRevision(account)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	second, _ := AccountCircuitDispatchRevision(account)
	if first != second || !strings.HasPrefix(first, "v1:") {
		t.Fatalf("revision mismatch: %s vs %s", first, second)
	}
	// Explicit numeric revisions win verbatim.
	numeric := int64(7)
	account.DispatchRevision = &numeric
	if got, _ := AccountCircuitDispatchRevision(account); got != "7" {
		t.Fatalf("numeric revision = %s", got)
	}
}

func TestFailureReasonWithEvidenceMarker(t *testing.T) {
	evidence := strings.Repeat("e", 64)
	got := failureReasonWithEvidence("transport:boom", &evidence)
	want := fmt.Sprintf("transport:boom|request_evidence_sha256=%s", evidence)
	if got != want {
		t.Fatalf("reason = %s, want %s", got, want)
	}
	state := State{FailureReason: strPtr(want)}
	if !sameRequestFailureEvidence(state, &evidence) {
		t.Fatalf("sameRequestFailureEvidence should match the reason marker")
	}
	other := strings.Repeat("f", 64)
	if sameRequestFailureEvidence(state, &other) {
		t.Fatalf("different evidence must not match")
	}
	if sameRequestFailureEvidence(State{}, nil) {
		t.Fatalf("empty state must not match")
	}
}
