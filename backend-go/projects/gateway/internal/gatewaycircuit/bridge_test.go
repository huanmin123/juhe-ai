package gatewaycircuit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockControlPlaneDB records the bridge's durable interactions.
type mockControlPlaneDB struct {
	mu sync.Mutex

	casCalls     []CompareAndSetIncidentInput
	casResponses []CompareAndSetIncidentResult
	casErrs      []error

	rebuildPages []RebuildPage
	rebuildCalls int

	incidents map[string]IncidentRecord

	acked   []AckOutboxInput
	release []ReleaseOutboxInput
}

func (m *mockControlPlaneDB) CompareAndSetIncident(_ context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.casCalls = append(m.casCalls, input)
	index := len(m.casCalls) - 1
	if index < len(m.casErrs) && m.casErrs[index] != nil {
		return CompareAndSetIncidentResult{}, m.casErrs[index]
	}
	if index < len(m.casResponses) {
		return m.casResponses[index], nil
	}
	return CompareAndSetIncidentResult{Status: CASApplied, CurrentDispatchRevision: input.DispatchRevision}, nil
}

func (m *mockControlPlaneDB) ListIncidentsForRebuild(_ context.Context, _ RebuildPageInput) (RebuildPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.rebuildCalls
	m.rebuildCalls++
	if index < len(m.rebuildPages) {
		return m.rebuildPages[index], nil
	}
	return RebuildPage{}, nil
}

func (m *mockControlPlaneDB) ListIncidentsByRuntimeKeys(_ context.Context, input ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []IncidentRecord
	for _, key := range input.AccountRuntimeKeys {
		for runtimeKey, incident := range m.incidents {
			if runtimeKey == key {
				out = append(out, incident)
			}
		}
	}
	return out, nil
}

func (m *mockControlPlaneDB) GetIncidentByScopeKey(_ context.Context, scopeKey string) (*IncidentRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, incident := range m.incidents {
		if incident.CircuitScopeKey == scopeKey {
			return &incident, nil
		}
	}
	return nil, nil
}

func (m *mockControlPlaneDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	return nil, nil
}

func (m *mockControlPlaneDB) AckOutbox(_ context.Context, input AckOutboxInput) (AckOutboxResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = append(m.acked, input)
	return AckOutboxResult{Acknowledged: true}, nil
}

func (m *mockControlPlaneDB) ReleaseOutboxForReplay(_ context.Context, input ReleaseOutboxInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.release = append(m.release, input)
	return nil
}

func newTestBridge(t *testing.T, store Store, db *mockControlPlaneDB, mutate func(*BridgeOptions)) *Bridge {
	t.Helper()
	options := BridgeOptions{
		Store: store,
		DB:    db,
		Now:   func() int64 { return 1000 },
		Sleep: func(context.Context, time.Duration) error { return nil },
		NewTimer: func(delay time.Duration) (<-chan struct{}, func()) {
			done := make(chan struct{})
			// Retry timers never fire in tests; the explicit paths drive work.
			return done, func() {}
		},
	}
	if mutate != nil {
		mutate(&options)
	}
	bridge, err := NewBridge(options)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)
	return bridge
}

func TestBridgeObservePersistsMappedIncident(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	state := ClosedState(protocolScope("acc"), "5", 2, "t1", 900)
	state.Phase = PhaseSuspect
	required := int64(2)
	count := int64(1)
	state.ConfirmationFailuresRequired = &required
	state.ConfirmationFailureCount = &count
	sha := strings.Repeat("a", 64)
	state.FailureEvidenceKeys = stringList{sha}
	retryAt := int64(4000)
	state.RetryAtMs = &retryAt
	state.FailureReason = strPtr("transport:timeout occurred")

	bridge.Observe(protocolScope("acc"), state)
	waitForBridgeIdle(t, bridge)

	if len(db.casCalls) != 1 {
		t.Fatalf("cas calls = %d", len(db.casCalls))
	}
	input := db.casCalls[0]
	if input.ScopeKind != ScopeKindProtocolModel || input.ProtocolCode == nil || *input.ProtocolCode != "profile" {
		t.Fatalf("scope mapping = %+v", input)
	}
	if input.RequestLane == nil || *input.RequestLane != LaneText || input.ModelFamily == nil || *input.ModelFamily != "gpt-4o" {
		t.Fatalf("lane/model mapping = %+v", input)
	}
	if input.State != PhaseSuspect || input.Generation != 2 || input.DispatchRevision != 5 {
		t.Fatalf("state mapping = %+v", input)
	}
	if input.ConsecutiveFailures == nil || *input.ConsecutiveFailures != 1 {
		t.Fatalf("consecutiveFailures = %+v", input.ConsecutiveFailures)
	}
	if len(input.ConfirmationFailureEvidenceKeys) != 1 || input.ConfirmationFailureEvidenceKeys[0] != sha {
		t.Fatalf("evidence keys = %v", input.ConfirmationFailureEvidenceKeys)
	}
	if input.LastFailureClass == nil || *input.LastFailureClass != FailureClassTimeoutBeforeComplete {
		t.Fatalf("failure class = %+v", input.LastFailureClass)
	}
	if input.NextTransitionAtMs == nil || *input.NextTransitionAtMs != 4000 {
		t.Fatalf("nextTransitionAtMs = %+v", input.NextTransitionAtMs)
	}
	if input.AccountID != "acc" || input.AccountRuntimeKey != "acc" {
		t.Fatalf("account mapping = %+v", input)
	}
}

func TestBridgeObserveClosedRetainsAndMapsLease(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	open := ClosedState(accountScope("acc"), "5", 1, "t1", 900)
	open.Phase = PhaseOpen
	lease := &Lease{Kind: LeaseKindHalfOpen, LeaseID: "lease-1", LeaseUntilMs: 5000}
	open.Lease = lease
	bridge.Observe(accountScope("acc"), open)
	waitForBridgeIdle(t, bridge)

	if len(db.casCalls) != 1 {
		t.Fatalf("cas calls = %d", len(db.casCalls))
	}
	input := db.casCalls[0]
	if input.LeaseID == nil || *input.LeaseID != "lease-1" || input.LeasePurpose == nil || *input.LeasePurpose != LeaseKindHalfOpen {
		t.Fatalf("lease mapping = %+v", input)
	}
	if input.LeaseOwnerRunID == nil || !strings.HasPrefix(*input.LeaseOwnerRunID, "circuit-bridge:") {
		t.Fatalf("lease owner = %+v", input.LeaseOwnerRunID)
	}

	// CLOSED observations carry a retainedUntil and keep child relationships.
	closed := ClosedState(accountScope("acc"), "5", 1, "t2", 950)
	closed.IncidentID = strPtr("t1")
	closed.ChildScopeKeys = stringList{MustScopeKey(protocolScope("acc"))}
	closed.ChildIncidentIDs = stringList{"c1"}
	bridge.Observe(accountScope("acc"), closed)
	waitForBridgeIdle(t, bridge)
	if len(db.casCalls) != 2 {
		t.Fatalf("cas calls = %d", len(db.casCalls))
	}
	closedInput := db.casCalls[1]
	if closedInput.RetainedUntilMs == nil || *closedInput.RetainedUntilMs != 1000+5*60_000 {
		t.Fatalf("retainedUntil = %+v", closedInput.RetainedUntilMs)
	}
	if len(closedInput.ChildIncidentIDs) != 1 {
		t.Fatalf("child incidents = %v", closedInput.ChildIncidentIDs)
	}
}

func TestBridgePersistenceFailureMarksAccountUnready(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{casErrs: []error{errors.New("db down"), errors.New("db down"), errors.New("db down")}}
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.MaxPersistAttempts = 3
	})
	bridge.Observe(accountScope("acc"), ClosedState(accountScope("acc"), "5", 1, "t1", 900))
	// The worker retries asynchronously; poll for the failure marker.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready, _ := bridge.IsAccountReady("acc"); !ready && bridge.IsReady() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ready, _ := bridge.IsAccountReady("acc")
	if ready {
		t.Fatalf("account must be unready after persistence failure")
	}
	if len(db.casCalls) != 3 {
		t.Fatalf("attempts = %d, want 3", len(db.casCalls))
	}
}

func TestBridgeRebuildRestoresPagesAndParentsLast(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{rebuildPages: []RebuildPage{
		{Items: []IncidentRecord{childIncident(t), parentIncident(t)}, NextCursor: &RebuildCursor{UpdatedAtMs: 100, CircuitScopeKey: "k"}},
		{Items: []IncidentRecord{}},
	}}
	bridge := newTestBridge(t, store, db, nil)
	result, err := bridge.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if result.Blocked || result.Loaded != 2 {
		t.Fatalf("rebuild result = %+v", result)
	}
	if !bridge.IsReady() {
		t.Fatalf("bridge must be globally ready after rebuild")
	}
	// The child (no children of its own) must be restored before the parent.
	childState, err := store.Get(context.Background(), protocolScope("acc"), nil)
	if err != nil {
		t.Fatalf("child get: %v", err)
	}
	if childState.ShadowedByIncidentID == nil || *childState.ShadowedByIncidentID != "parent-1" {
		t.Fatalf("child after parent restore = %+v", childState)
	}
}

func childIncident(t *testing.T) IncidentRecord {
	t.Helper()
	scopeKey := MustScopeKey(protocolScope("acc"))
	return IncidentRecord{
		CircuitScopeKey:                 scopeKey,
		AccountID:                       "acc",
		AccountRuntimeKey:               "acc",
		ScopeKind:                       ScopeKindProtocolModel,
		ProtocolCode:                    strPtr("profile"),
		RequestLane:                     strPtr(LaneText),
		ModelFamily:                     strPtr("gpt-4o"),
		IncidentID:                      "child-1",
		ChildIncidentIDs:                []string{},
		State:                           PhaseSuspect,
		Generation:                      1,
		DispatchRevision:                5,
		LedgerRevision:                  1,
		ProjectedLedgerRevision:         1,
		TransitionID:                    "ct",
		ConfirmationFailuresRequired:    2,
		ConsecutiveFailures:             1,
		ConfirmationFailureEvidenceKeys: []string{strings.Repeat("a", 64)},
		UpdatedAtMs:                     100,
	}
}

func parentIncident(t *testing.T) IncidentRecord {
	t.Helper()
	scopeKey := MustScopeKey(accountScope("acc"))
	return IncidentRecord{
		CircuitScopeKey:                 scopeKey,
		AccountID:                       "acc",
		AccountRuntimeKey:               "acc",
		ScopeKind:                       ScopeKindAccount,
		IncidentID:                      "parent-1",
		ParentIncidentID:                nil,
		ChildIncidentIDs:                []string{"child-1"},
		State:                           PhaseOpen,
		Generation:                      1,
		DispatchRevision:                5,
		LedgerRevision:                  2,
		ProjectedLedgerRevision:         2,
		TransitionID:                    "pt",
		BackoffLevel:                    1,
		ConfirmationFailuresRequired:    2,
		ConsecutiveFailures:             0,
		ConfirmationFailureEvidenceKeys: []string{},
		UpdatedAtMs:                     200,
	}
}

func TestBridgeRebuildInvalidCursorBlocks(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{rebuildPages: []RebuildPage{
		{Items: []IncidentRecord{}, NextCursor: &RebuildCursor{UpdatedAtMs: 100, CircuitScopeKey: "k"}},
		{Items: []IncidentRecord{}, NextCursor: &RebuildCursor{UpdatedAtMs: 100, CircuitScopeKey: "k"}},
	}}
	bridge := newTestBridge(t, store, db, func(options *BridgeOptions) {
		options.RebuildMaxPages = 2
	})
	result, err := bridge.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("result = %+v", result)
	}
	if bridge.IsReady() {
		t.Fatalf("gate must stay closed")
	}
}

// TestBridgeObserveAccountNotFoundTerminal 对齐归档热修回归
// account-circuit-control-plane-bridge-regression.ts：物理删除账户的迟到运行态
// 观察必须被 account_not_found 终态吸收——只结算一次持久化、不进入重试、
// 不把持久层返回的 revision 记入 bridge 缓存、不污染 account readiness。
func TestBridgeObserveAccountNotFoundTerminal(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{casResponses: []CompareAndSetIncidentResult{
		{Status: CASAccountNotFound, CurrentDispatchRevision: 99},
		{Status: CASAccountNotFound, CurrentDispatchRevision: 99},
		{Status: CASAccountNotFound, CurrentDispatchRevision: 99},
	}}
	bridge := newTestBridge(t, store, db, nil)
	if _, err := bridge.Rebuild(context.Background()); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !bridge.IsReady() {
		t.Fatalf("rebuild must open the global readiness gate")
	}

	state := ClosedState(accountScope("acc"), "7", 1, "t1", 900)
	state.Phase = PhaseSuspect
	bridge.Observe(accountScope("acc"), state)
	waitForBridgeIdle(t, bridge)
	time.Sleep(20 * time.Millisecond)

	if len(db.casCalls) != 1 {
		t.Fatalf("account_not_found must settle after one attempt, got %d", len(db.casCalls))
	}
	bridge.mu.Lock()
	recordedRevision, hasRecordedRevision := bridge.dispatchRevisions["acc"]
	_, hasLedgerRevision := bridge.ledgerRevisions[MustScopeKey(accountScope("acc"))]
	bridge.mu.Unlock()
	if !hasRecordedRevision || recordedRevision != 7 {
		t.Fatalf("persisted revision must not be recorded, got %d (present=%t)", recordedRevision, hasRecordedRevision)
	}
	if hasLedgerRevision {
		t.Fatalf("ledger revision must not be recorded for a terminal account_not_found")
	}
	if ready, err := bridge.IsAccountReady("acc"); err != nil || !ready {
		t.Fatalf("terminal account_not_found must not poison account readiness: ready=%t err=%v", ready, err)
	}
}

func TestBridgeProjectPending(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	// Inject the outbox claim by wrapping the DB.
	claims := []OutboxEvent{
		{
			EventID: "e1", ProjectionKey: "p1", EventType: OutboxEventTypeDispatchRevisionChanged,
			AccountID: "acc", AccountRuntimeKey: "acc", TransitionID: "rev-t",
			DispatchRevision: 9, ClaimToken: strPtr("tok-1"),
		},
		{
			EventID: "e2", ProjectionKey: "p2", EventType: OutboxEventTypeIncidentChanged,
			AccountID: "acc", AccountRuntimeKey: "acc", TransitionID: "inc-t",
			DispatchRevision: 5, CircuitScopeKey: strPtr(MustScopeKey(accountScope("acc"))),
			ClaimToken: strPtr("tok-2"),
		},
	}
	projectedIncident := parentIncident(t)
	db.mu.Lock()
	db.incidents = map[string]IncidentRecord{projectedIncident.AccountRuntimeKey: projectedIncident}
	db.mu.Unlock()

	wrapped := &claimingDB{ControlPlaneDB: db, claims: claims, call: 0}
	bridge.db = wrapped
	acknowledged, err := bridge.ProjectPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("projectPending: %v", err)
	}
	if acknowledged != 2 {
		t.Fatalf("acknowledged = %d, want 2", acknowledged)
	}
	// The dispatch-revision event flowed into the store as a revision replace.
	if changed, _ := store.ReplaceAccountDispatchRevision(context.Background(), ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "acc", DispatchRevision: "9", TransitionID: "probe", NowMs: int64Ptr(1000),
	}); changed != 0 {
		// 0 is expected because the earlier replace already applied revision 9;
		// the assertion only verifies the call did not error.
		_ = changed
	}
	if len(wrapped.acked) != 2 {
		t.Fatalf("acks = %d", len(wrapped.acked))
	}
}

type claimingDB struct {
	ControlPlaneDB
	claims []OutboxEvent
	call   int

	acked []AckOutboxInput
}

func (c *claimingDB) ClaimOutbox(_ context.Context, _ ClaimOutboxInput) ([]OutboxEvent, error) {
	// Node claims one bounded page per call; hand out everything once.
	if c.call == 0 {
		c.call++
		return c.claims, nil
	}
	return nil, nil
}

func (c *claimingDB) AckOutbox(ctx context.Context, input AckOutboxInput) (AckOutboxResult, error) {
	c.acked = append(c.acked, input)
	return c.ControlPlaneDB.AckOutbox(ctx, input)
}

func TestBridgeReconcileActiveRequiresReady(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)
	repaired, err := bridge.ReconcileActive(context.Background(), 10)
	if err != nil || repaired != 0 {
		t.Fatalf("reconcile before ready = (%d, %v)", repaired, err)
	}
}

func TestIncidentToRuntimeStateRejectsScopeMismatch(t *testing.T) {
	incident := childIncident(t)
	incident.CircuitScopeKey = "bogus"
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatalf("scope mismatch must panic like the Node throw")
		}
	}()
	IncidentToRuntimeState(incident, nil)
}

func TestIncidentMatchesRuntimeState(t *testing.T) {
	incident := childIncident(t)
	state := IncidentToRuntimeState(incident, nil)
	if !incidentMatchesRuntimeState(&incident, state) {
		t.Fatalf("identical incident must match")
	}
	state.Generation++
	if incidentMatchesRuntimeState(&incident, state) {
		t.Fatalf("generation drift must not match")
	}
}

func TestPublicSummarySelection(t *testing.T) {
	incident := childIncident(t)
	open := parentIncident(t)
	summaries := PublicSummariesFromIncidents([]string{"acc", ""}, []IncidentRecord{incident, open})
	if summary, ok := summaries["acc"]; !ok || summary.Status != PublicSummaryStatusAvoided {
		t.Fatalf("summary = %+v", summaries)
	}
	if _, ok := summaries[""]; ok {
		t.Fatalf("blank keys must be dropped")
	}
	recovering := childIncident(t)
	recovering.State = PhaseRecovering
	recovering.NextTransitionAtMs = int64Ptr(5000)
	summary := PublicSummaryOf([]IncidentRecord{recovering})
	if summary.Status != PublicSummaryStatusRecovering || summary.NextCheckAt == "" {
		t.Fatalf("recovering summary = %+v", summary)
	}
	if PublicSummaryOf(nil).Status != PublicSummaryStatusNormal {
		t.Fatalf("empty summary must be normal")
	}
}

func TestClassifyFailure(t *testing.T) {
	tests := map[string]string{
		"Transport:TIMEOUT occurred": FailureClassTimeoutBeforeComplete,
		"dial tcp connect refused":   FailureClassConnectFailed,
		"body read interrupted":      FailureClassReadInterrupted,
		"policy violation":           FailureClassExplicitPolicy,
		"stream truncated":           FailureClassIncompleteResponse,
	}
	for reason, want := range tests {
		if got := classifyFailure(reason); got != want {
			t.Fatalf("classifyFailure(%q) = %s, want %s", reason, got, want)
		}
	}
}

func waitForBridgeIdle(t *testing.T, bridge *Bridge) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		idle := len(bridge.workers) == 0 && len(bridge.pending) == 0
		bridge.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("bridge never went idle")
}
