package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockHotQualityStore records calls and replays scripted results (Mock 闭环).
// The lifecycle records attempts from a background goroutine, so call
// bookkeeping is mutex-guarded.
type mockHotQualityStore struct {
	t *testing.T

	mu              sync.Mutex
	attemptResults  []*HotQualityAttemptMutationResult
	attemptErr      error
	attemptCalls    []HotQualityRecordAttemptInput
	terminalResults []*HotQualityTerminalMutationResult
	terminalErr     error
	terminalCalls   []HotQualityRecordTerminalInput

	snapshots  []*HotQualitySnapshot
	snapshotOf func(scope HotQualityScope) (*HotQualitySnapshot, error)

	terminalRecords []*HotQualityTerminalRecord
	stats           *HotQualityStoreStats
}

func (m *mockHotQualityStore) RecordAttempt(ctx context.Context, input HotQualityRecordAttemptInput) (*HotQualityAttemptMutationResult, error) {
	m.mu.Lock()
	m.attemptCalls = append(m.attemptCalls, input)
	attemptErr := m.attemptErr
	var result *HotQualityAttemptMutationResult
	if attemptErr == nil && len(m.attemptResults) > 0 {
		result = m.attemptResults[0]
		m.attemptResults = m.attemptResults[1:]
	}
	m.mu.Unlock()
	if attemptErr != nil {
		return nil, attemptErr
	}
	if result == nil {
		return &HotQualityAttemptMutationResult{Status: AttemptMutationApplied, RequestedScope: input.Scope, EffectiveScope: input.Scope}, nil
	}
	return result, nil
}

func (m *mockHotQualityStore) RecordTerminal(ctx context.Context, input HotQualityRecordTerminalInput) (*HotQualityTerminalMutationResult, error) {
	m.mu.Lock()
	m.terminalCalls = append(m.terminalCalls, input)
	terminalErr := m.terminalErr
	var result *HotQualityTerminalMutationResult
	if terminalErr == nil && len(m.terminalResults) > 0 {
		result = m.terminalResults[0]
		m.terminalResults = m.terminalResults[1:]
	}
	m.mu.Unlock()
	if terminalErr != nil {
		return nil, terminalErr
	}
	if result == nil {
		return &HotQualityTerminalMutationResult{Status: TerminalMutationApplied}, nil
	}
	return result, nil
}

func (m *mockHotQualityStore) recordedAttempts() []HotQualityRecordAttemptInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]HotQualityRecordAttemptInput{}, m.attemptCalls...)
}

func (m *mockHotQualityStore) recordedTerminals() []HotQualityRecordTerminalInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]HotQualityRecordTerminalInput{}, m.terminalCalls...)
}

func (m *mockHotQualityStore) Get(ctx context.Context, scope HotQualityScope, nowMs *int64) (*HotQualitySnapshot, error) {
	if m.snapshotOf != nil {
		return m.snapshotOf(scope)
	}
	return nil, nil
}

func (m *mockHotQualityStore) GetTerminal(ctx context.Context, attemptID string, nowMs *int64) (*HotQualityTerminalRecord, error) {
	return nil, nil
}

func (m *mockHotQualityStore) Stats(ctx context.Context, nowMs *int64) (*HotQualityStoreStats, error) {
	return m.stats, nil
}

type recordingObserver struct {
	mu     sync.Mutex
	events []RoutingObservation
}

func (o *recordingObserver) ObserveGatewayRouting(observation RoutingObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, observation)
}

func (o *recordingObserver) snapshot() []RoutingObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]RoutingObservation{}, o.events...)
}

type recordingLogger struct {
	warnings []string
	fields   []map[string]interface{}
}

func (l *recordingLogger) Warn(fields map[string]interface{}, msg string) {
	l.warnings = append(l.warnings, msg)
	l.fields = append(l.fields, fields)
}

func newTestRuntime(store HotQualityStore, exploration SameTierExplorationStore) (*GatewayHotQualityRuntime, *recordingObserver, *recordingLogger) {
	observer := &recordingObserver{}
	logger := &recordingLogger{}
	return &GatewayHotQualityRuntime{
		HotQualityStore:  store,
		ExplorationStore: exploration,
		Observer:         observer,
		Logger:           logger,
	}, observer, logger
}

func TestNewGatewayHotQualityAttemptLifecycleValidation(t *testing.T) {
	runtime, _, _ := newTestRuntime(&mockHotQualityStore{t: t}, nil)
	if _, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{Runtime: runtime, AttemptID: " ", Account: GatewayHotQualityAccountView{ID: "a"}}); err == nil || err.Error() != "热质量 attemptId 不能为空" {
		t.Fatalf("err = %v", err)
	}
	// Go composition-root guard replacing the Node module-singleton fallback.
	if _, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{AttemptID: "a", Account: GatewayHotQualityAccountView{ID: "a", ProtocolCode: "openai", ProtocolVersion: "2024"}}); err == nil || err.Error() != "热质量运行时不能为空" {
		t.Fatalf("err = %v", err)
	}
	// an authorized account without binding context is rejected
	if _, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
		Runtime: runtime, AttemptID: "a",
		Account: GatewayHotQualityAccountView{ID: "a", ProtocolCode: "openai", ProtocolVersion: "2024", AccountAccessType: "account_authorized"},
	}); err == nil || err.Error() != "授权账户运行态键缺少绑定上下文" {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayHotQualityAttemptLifecycleHappyPath(t *testing.T) {
	store := &mockHotQualityStore{t: t}
	runtime, observer, logger := newTestRuntime(store, nil)
	model := "gpt-5"
	lifecycle, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
		Runtime:     runtime,
		AttemptID:   " at-1 ",
		Account:     GatewayHotQualityAccountView{ID: "acc-1", ProviderProtocolProfileID: "openai:2024", FallbackEnabled: true, Priority: 2},
		RequestLane: "text",
		Model:       &model,
		NowMs:       int64Ptr(5_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lifecycle.AttemptID != "at-1" {
		t.Fatalf("attemptID = %s", lifecycle.AttemptID)
	}
	// wait for the async attempt record
	deadline := time.Now().Add(time.Second)
	for len(store.recordedAttempts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	attemptCalls := store.recordedAttempts()
	if len(attemptCalls) != 1 {
		t.Fatalf("attempt calls = %d", len(attemptCalls))
	}
	if attemptCalls[0].Scope != (HotQualityScope{AccountRuntimeKey: "acc-1", ProtocolProfile: "openai:2024", RequestLane: "text", ModelFamily: gatewayModelFamilyFor(t, "gpt-5")}) {
		t.Fatalf("scope = %+v", attemptCalls[0].Scope)
	}
	if attemptCalls[0].NowMs == nil || *attemptCalls[0].NowMs != 5_000 {
		t.Fatalf("nowMs = %v", attemptCalls[0].NowMs)
	}

	// first byte: first write wins, invalid values ignored
	valid := 1234.6
	lifecycle.MarkFirstByte(&valid)
	invalid := -5.0
	lifecycle.MarkFirstByte(&invalid)
	other := 999.0
	lifecycle.MarkFirstByte(&other)

	lifecycle.RecordTerminal(context.Background(), GatewayHotQualityTerminalInput{OutcomeClass: TerminalOutcomeCompletedResponse})
	terminalCalls := store.recordedTerminals()
	if len(terminalCalls) != 1 {
		t.Fatalf("terminal calls = %d", len(terminalCalls))
	}
	terminalCall := terminalCalls[0]
	if terminalCall.TerminalOutcomeID != "at-1:terminal" {
		t.Fatalf("terminalOutcomeId = %s", terminalCall.TerminalOutcomeID)
	}
	if terminalCall.FailureScope != FailureScopeNone || terminalCall.Source != TerminalSourceRequestLifecycle {
		t.Fatalf("defaults = %s/%s", terminalCall.FailureScope, terminalCall.Source)
	}
	if terminalCall.FirstByteMs == nil || *terminalCall.FirstByteMs != 1235 {
		t.Fatalf("firstByte = %v", terminalCall.FirstByteMs)
	}

	// events: attempt started, mutation applied, attempt completed, terminal mutation applied
	want := []RoutingObservation{
		{Kind: "attempt", Outcome: "started"},
		{Kind: "hot_quality_mutation", Operation: "attempt", Status: "applied"},
		{Kind: "attempt", Outcome: "completed"},
		{Kind: "hot_quality_mutation", Operation: "terminal", Status: "applied"},
	}
	waitDeadline := time.Now().Add(time.Second)
	for len(observer.snapshot()) < len(want) && time.Now().Before(waitDeadline) {
		time.Sleep(time.Millisecond)
	}
	if fmt.Sprint(observer.snapshot()) != fmt.Sprint(want) {
		t.Fatalf("events = %+v, want %+v", observer.snapshot(), want)
	}
	if len(logger.warnings) != 0 {
		t.Fatalf("warnings = %v", logger.warnings)
	}

	// memoized: second RecordTerminal must not hit the store again
	lifecycle.RecordTerminal(context.Background(), GatewayHotQualityTerminalInput{OutcomeClass: TerminalOutcomeTimeout})
	if len(store.recordedTerminals()) != 1 {
		t.Fatalf("terminal must be memoized, calls = %d", len(store.recordedTerminals()))
	}
}

func gatewayModelFamilyFor(t *testing.T, model string) string {
	t.Helper()
	return GatewayHotQualityModelFamily(&model)
}

func gatewayModelFamilyForNil() string {
	return GatewayHotQualityModelFamily(nil)
}

func TestGatewayHotQualityAttemptLifecycleErrorPaths(t *testing.T) {
	t.Run("attempt record failure warns and swallows", func(t *testing.T) {
		store := &mockHotQualityStore{t: t, attemptErr: errors.New("redis down")}
		runtime, observer, logger := newTestRuntime(store, nil)
		lifecycle, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
			Runtime:     runtime,
			AttemptID:   "at-err",
			Account:     GatewayHotQualityAccountView{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "2024"},
			RequestLane: "text",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lifecycle.RecordTerminal(context.Background(), GatewayHotQualityTerminalInput{OutcomeClass: TerminalOutcomeTimeout})
		if len(logger.warnings) != 1 || logger.warnings[0] != "记录热质量 attempt 失败" {
			t.Fatalf("warnings = %v", logger.warnings)
		}
		if logger.fields[0]["event"] != "gateway_hot_quality_attempt_record_failed" {
			t.Fatalf("fields = %+v", logger.fields[0])
		}
		// attempt mutation observation is skipped on error (the terminal one
		// still runs because the mock terminal store succeeds)
		deadline := time.Now().Add(time.Second)
		for len(store.recordedTerminals()) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		for _, event := range observer.snapshot() {
			if event.Kind == "hot_quality_mutation" && event.Operation == "attempt" {
				t.Fatalf("unexpected attempt mutation observation: %+v", event)
			}
		}
	})

	t.Run("terminal record failure warns with outcome", func(t *testing.T) {
		store := &mockHotQualityStore{t: t, terminalErr: errors.New("redis down")}
		runtime, observer, logger := newTestRuntime(store, nil)
		lifecycle, err := NewGatewayHotQualityAttemptLifecycle(GatewayHotQualityAttemptLifecycleInput{
			Runtime:     runtime,
			AttemptID:   "at-err2",
			Account:     GatewayHotQualityAccountView{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "2024"},
			RequestLane: "image",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lifecycle.RecordTerminal(context.Background(), GatewayHotQualityTerminalInput{OutcomeClass: TerminalOutcomeReadInterruption})
		deadline := time.Now().Add(time.Second)
		for len(logger.warnings) < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(logger.warnings) != 1 || logger.warnings[0] != "记录热质量终态失败" {
			t.Fatalf("warnings = %v", logger.warnings)
		}
		if logger.fields[0]["event"] != "gateway_hot_quality_terminal_record_failed" || logger.fields[0]["outcomeClass"] != TerminalOutcomeReadInterruption {
			t.Fatalf("fields = %+v", logger.fields[0])
		}
		// attempt outcome observed before the failed terminal record
		found := false
		for _, event := range observer.snapshot() {
			if event.Kind == "attempt" && event.Outcome == "transport_failure" {
				found = true
			}
		}
		if !found {
			t.Fatalf("attempt observation missing: %+v", observer.events)
		}
	})
}

func TestGatewayHotQualityObservationStatusMapping(t *testing.T) {
	testCases := []struct {
		status string
		want   string
	}{
		{"applied", "applied"},
		{"idempotent", "idempotent"},
		{"degraded_to_protocol", "unavailable"},
		{"attempt_conflict", "conflict"},
		{"terminal_outcome_conflict", "conflict"},
		{"key_capacity_exhausted", "capacity_exhausted"},
		{"attempt_capacity_exhausted", "capacity_exhausted"},
		{"attempt_not_found", "unavailable"},
		{"quality_key_unavailable", "unavailable"},
		{"terminal_conflict", "conflict"},
	}
	for _, testCase := range testCases {
		if got := hotQualityObservationStatus(testCase.status); got != testCase.want {
			t.Fatalf("hotQualityObservationStatus(%s) = %s, want %s", testCase.status, got, testCase.want)
		}
	}
}

func TestGatewayHotQualityTerminalObservationMapping(t *testing.T) {
	testCases := []struct {
		outcome string
		want    string
	}{
		{TerminalOutcomeCompletedResponse, "completed"},
		{TerminalOutcomeExplicitPolicyFailure, "completed"},
		{TerminalOutcomeClientCancellation, "client_canceled"},
		{TerminalOutcomeUpstreamResponseFailure, "unknown"},
		{TerminalOutcomeUnknown, "unknown"},
		{TerminalOutcomeTimeout, "transport_failure"},
		{TerminalOutcomeTransportFailure, "transport_failure"},
		{TerminalOutcomeReadInterruption, "transport_failure"},
		{TerminalOutcomeIncompleteResponse, "transport_failure"},
	}
	for _, testCase := range testCases {
		if got := terminalAttemptObservation(testCase.outcome); got != testCase.want {
			t.Fatalf("terminalAttemptObservation(%s) = %s, want %s", testCase.outcome, got, testCase.want)
		}
	}
}

func TestGatewayAccountRuntimeKey(t *testing.T) {
	if key, err := GatewayAccountRuntimeKey(GatewayHotQualityAccountView{ID: "acc"}); err != nil || key != "acc" {
		t.Fatalf("key = %s, err = %v", key, err)
	}
	key, err := GatewayAccountRuntimeKey(GatewayHotQualityAccountView{
		ID: "acc", AccountAccessType: "account_authorized",
		BindingSystemAccountID: "sys", BoundGroupID: "g1", AccountAuthorizationID: "az",
	})
	if err != nil || key != "acc:authorized:sys:g1:az" {
		t.Fatalf("key = %s, err = %v", key, err)
	}
	if _, err := GatewayAccountRuntimeKey(GatewayHotQualityAccountView{ID: "acc", AccessType: "authorized"}); err == nil || err.Error() != "授权账户运行态键缺少绑定上下文" {
		t.Fatalf("err = %v", err)
	}
}
