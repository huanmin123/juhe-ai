package gatewayaccounteffects

import (
	"context"
	"sync"
	"testing"
	"time"
)

type mockKeyModelStore struct {
	mu               sync.Mutex
	admissions       map[string]KeyModelAdmissionResult
	renewed          bool
	renewLost        bool
	releases         []string
	failures         []KeyModelFailureIntent
	mainProbeFences  []string
	j1Claimed        bool
	j1Result         bool
	recordFailureErr error
}

func newMockKeyModelStore() *mockKeyModelStore {
	return &mockKeyModelStore{admissions: map[string]KeyModelAdmissionResult{}, j1Result: true}
}

func (m *mockKeyModelStore) Get(context.Context, CapabilityKey) (*KeyModelState, error) { return nil, nil }

func (m *mockKeyModelStore) RecordFailure(_ context.Context, intent KeyModelFailureIntent) (KeyModelFailureResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recordFailureErr != nil {
		return KeyModelFailureResult{}, m.recordFailureErr
	}
	m.failures = append(m.failures, intent)
	state, err := CreateKeyModelOpenState(intent.Capability, intent.ObservedAtMs)
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	return KeyModelFailureResult{Status: KeyModelMutationApplied, State: &state, Applied: true}, nil
}

func (m *mockKeyModelStore) AdmitForeground(_ context.Context, capability CapabilityKey, attemptID string) (KeyModelAdmissionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hash := mustCapabilityHash(capability)
	if result, ok := m.admissions[hash+"|"+attemptID]; ok {
		return result, nil
	}
	permit := &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: attemptID, LeaseUntilMs: 1_000}
	m.admissions[hash+"|"+attemptID] = KeyModelAdmissionResult{Status: ForegroundAdmitted, Permit: permit}
	return m.admissions[hash+"|"+attemptID], nil
}

func (m *mockKeyModelStore) ReleaseForeground(_ context.Context, permit KeyModelForegroundPermit) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases = append(m.releases, permit.AttemptID)
	return true, nil
}

func (m *mockKeyModelStore) RenewForeground(context.Context, KeyModelForegroundPermit) (*KeyModelForegroundPermit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renewLost {
		return nil, nil
	}
	m.renewed = true
	return &KeyModelForegroundPermit{AttemptID: "renewed"}, nil
}

func (m *mockKeyModelStore) RecordMainProbeFailure(_ context.Context, capability CapabilityKey, permit KeyModelForegroundPermit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mainProbeFences = append(m.mainProbeFences, permit.AttemptID)
	return nil
}

func (m *mockKeyModelStore) ClearMainProbeFence(context.Context, KeyModelFenceReference, string) (bool, error) {
	return true, nil
}

func (m *mockKeyModelStore) DeferMainProbeFence(context.Context, KeyModelFenceReference) (bool, error) {
	return true, nil
}

func (m *mockKeyModelStore) ClaimJ1Confirmation(context.Context, string, int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.j1Claimed = true
	return m.j1Result, nil
}

type recordedDispatcher struct {
	mu        sync.Mutex
	accountID string
	reason    string
	fence     *KeyModelFenceReference
}

func (d *recordedDispatcher) DispatchAccountHealthCheck(accountID string, reason string, fence *KeyModelFenceReference) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accountID, d.reason, d.fence = accountID, reason, fence
}

func prepareAttemptForTest(t *testing.T, store KeyModelRuntimeStore, route GatewayKeyModelCapability, budget *GatewayKeyModelFailureBudget) (*GatewayKeyModelAttempt, GatewayKeyModelAttemptPreparation, *ManualScheduler) {
	t.Helper()
	scheduler := NewManualScheduler()
	preparation, err := PrepareGatewayKeyModelAttempt(context.Background(), store, PrepareGatewayKeyModelAttemptInput{
		Route:         route,
		RequestID:     "req-1",
		AttemptID:     "attempt-1",
		FailureBudget: budget,
		Scheduler:     scheduler,
		Logger:        NopLogger{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return preparation.Attempt, preparation, scheduler
}

var _ = time.Second

func TestGatewayKeyModelFailureBudgetClaim(t *testing.T) {
	budget := NewGatewayKeyModelFailureBudget()
	if !budget.Claim("hash-1") {
		t.Fatal("first claim should succeed")
	}
	if budget.Claim("hash-1") {
		t.Fatal("duplicate claim must fail")
	}
	for index := 1; index < keyModelFailureIntentLimit; index++ {
		if !budget.Claim(string(rune('a'+index))) {
			t.Fatalf("claim %d should succeed", index)
		}
	}
	if budget.Claim("overflow") {
		t.Fatalf("claim beyond limit %d must fail", keyModelFailureIntentLimit)
	}
}

func TestAttemptCompleteSuccessReleasesPermit(t *testing.T) {
	store := newMockKeyModelStore()
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, preparation, scheduler := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	if preparation.Status != AttemptPreparationAdmitted {
		t.Fatalf("preparation = %+v", preparation)
	}
	if scheduler.Pending() != 1 {
		t.Fatalf("renewal timer = %d, want 1", scheduler.Pending())
	}
	if err := attempt.ReportCompleteSuccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	releases := store.releases
	store.mu.Unlock()
	if len(releases) != 1 || releases[0] != "attempt-1" {
		t.Fatalf("releases = %v", releases)
	}
	// 终态只结算一次：重复上报不再触发释放。
	if err := attempt.ReportUnknown(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.releases) != 1 {
		t.Fatalf("double settle releases = %v", store.releases)
	}
}

func TestAttemptUpstreamNotCompleteRecordsIntentAndDispatchesJ1(t *testing.T) {
	store := newMockKeyModelStore()
	dispatcher := &recordedDispatcher{}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}
	attempt, _, _ := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	attempt.SetDispatcher(dispatcher)

	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	failures := store.failures
	j1 := store.j1Claimed
	store.mu.Unlock()
	if len(failures) != 1 {
		t.Fatalf("failures = %d", len(failures))
	}
	intent := failures[0]
	if intent.IntentID != "req-1:attempt-1" || intent.Outcome != KeyModelOutcomeUpstreamNotComplete {
		t.Fatalf("intent = %+v", intent)
	}
	if intent.Permit == nil || intent.Permit.AttemptID != "attempt-1" {
		t.Fatalf("permit on intent = %+v", intent.Permit)
	}
	// sourceFence = sha256(`${credentialSourceAccountId}:${revision}`)。
	if intent.SourceFence != sourceFence(route) {
		t.Fatalf("sourceFence = %s", intent.SourceFence)
	}
	if !j1 {
		t.Fatal("J1 confirmation should be claimed after applied failure")
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.accountID != "source-1" || dispatcher.reason != "request_failure" || dispatcher.fence != nil {
		t.Fatalf("dispatch = %s %s %+v", dispatcher.accountID, dispatcher.reason, dispatcher.fence)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.releases) != 0 {
		t.Fatalf("recordFailure consumes the permit, releases = %v", store.releases)
	}
}

func TestAttemptBudgetExhaustedFallsBackToUnknown(t *testing.T) {
	store := newMockKeyModelStore()
	budget := NewGatewayKeyModelFailureBudget()
	if !budget.Claim(mustCapabilityHash(testCapability())) {
		t.Fatal("pre-claim should succeed")
	}
	attempt, _, _ := prepareAttemptForTest(t, store, GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}, budget)
	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.failures) != 0 {
		t.Fatalf("budget-blocked intent must not write, failures = %d", len(store.failures))
	}
	if len(store.releases) != 1 {
		t.Fatalf("fallback unknown must release, releases = %v", store.releases)
	}
}

func TestAttemptMainProbeWritesFence(t *testing.T) {
	store := newMockKeyModelStore()
	dispatcher := &recordedDispatcher{}
	route := GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability(), IsMainProbe: true}
	attempt, _, _ := prepareAttemptForTest(t, store, route, NewGatewayKeyModelFailureBudget())
	attempt.SetDispatcher(dispatcher)

	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.mainProbeFences) != 1 || len(store.failures) != 0 {
		t.Fatalf("fences = %v failures = %v", store.mainProbeFences, store.failures)
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.fence == nil || dispatcher.fence.OwnerID != "attempt-1" || dispatcher.fence.DispatchRevision != 3 || dispatcher.fence.KeyFingerprint != "fp-1" {
		t.Fatalf("fence = %+v", dispatcher.fence)
	}
	if dispatcher.fence.CapabilityHash != mustCapabilityHash(testCapability()) {
		t.Fatalf("fence hash = %s", dispatcher.fence.CapabilityHash)
	}
}

func TestAttemptRenewalLoopAndPermitLoss(t *testing.T) {
	store := newMockKeyModelStore()
	store.renewLost = true
	attempt, _, scheduler := prepareAttemptForTest(t, store, GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}, NewGatewayKeyModelFailureBudget())
	permitLost := make(chan struct{})
	attempt.SetPermitLostCallback(func() { close(permitLost) })
	attempt.SetObservability(scheduler, NopLogger{})

	scheduler.Fire() // 续租定时器：renew 失败 → losePermit。
	select {
	case <-permitLost:
	case <-time.After(time.Second):
		t.Fatal("permit loss must fire the callback")
	}
	select {
	case <-attempt.PermitLost():
	default:
		t.Fatal("PermitLost channel must be closed")
	}
	// 租约丢失后的失败观测按 unknown 结算（不写 failure intent）。
	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.failures) != 0 || len(store.releases) != 1 {
		t.Fatalf("failures = %v releases = %v", store.failures, store.releases)
	}
}

func TestAttemptRenewalSuccessReschedules(t *testing.T) {
	store := newMockKeyModelStore()
	_, _, scheduler := prepareAttemptForTest(t, store, GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}, NewGatewayKeyModelFailureBudget())
	scheduler.Fire()
	store.mu.Lock()
	renewed := store.renewed
	store.mu.Unlock()
	if !renewed {
		t.Fatal("renewal should have run")
	}
	// 续租成功后重新排程。
	if scheduler.Pending() != 1 {
		t.Fatalf("pending renewal timers = %d, want 1", scheduler.Pending())
	}
}

func TestAttemptRecordFailureErrorFallsBackToRelease(t *testing.T) {
	store := newMockKeyModelStore()
	store.recordFailureErr = errRedisStateURLRequired()
	attempt, _, _ := prepareAttemptForTest(t, store, GatewayKeyModelCapability{AccountID: "acc-1", Capability: testCapability()}, NewGatewayKeyModelFailureBudget())
	if err := attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.releases) != 1 {
		t.Fatalf("error path must release safely, releases = %v", store.releases)
	}
}

func TestMemoryRecoveryRunnerSweepLifecycle(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewInMemoryKeyModelRuntimeStore(clock)
	now := NowMs(clock)
	capabilityA := testCapability()
	capabilityB := testCapability()
	capabilityB.ClientModel = "other-model"
	for index, capability := range []CapabilityKey{capabilityA, capabilityB} {
		intent := memoryIntent(capability, "intent-"+string(rune('0'+index)), now)
		intent.RecoveryTarget = &KeyModelRecoveryTarget{AccountID: "source-1", GroupID: "grp-1", SystemAccountID: "sys-1"}
		if _, err := store.RecordFailure(context.Background(), intent); err != nil {
			t.Fatal(err)
		}
	}
	probes := 0
	var probeMu sync.Mutex
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store: store,
		Probe: func(input KeyModelRecoveryProbeInput) KeyModelOutcome {
			probeMu.Lock()
			probes++
			probeMu.Unlock()
			if input.Ctx == nil {
				t.Error("probe context missing")
			}
			return KeyModelOutcomeCompleteSuccess
		},
		Now:      func() int64 { return NowMs(clock) },
		CreateID: func() string { return "lease" },
	})

	// 未到期：无 due。
	result := runner.Sweep(context.Background())
	if result.DueCount != 0 || result.StartedCount != 0 {
		t.Fatalf("early sweep = %+v", result)
	}
	// 到期：两个状态都启动并 settle 为 RECOVERING(count=1)。
	clock.Advance(6 * time.Second)
	result = runner.Sweep(context.Background())
	if result.DueCount != 2 || result.StartedCount != 2 || result.SettledCount != 2 {
		t.Fatalf("sweep = %+v", result)
	}
	probeMu.Lock()
	if probes != 2 {
		probeMu.Unlock()
		t.Fatalf("probes = %d", probes)
	}
	probeMu.Unlock()
	// 同一 runner 串行复用：running 集合正确清理。
	clock.Advance(11 * time.Second)
	result = runner.Sweep(context.Background())
	if result.StartedCount != 2 {
		t.Fatalf("second sweep = %+v", result)
	}
}

func TestMemoryRecoveryRunnerConcurrencyLimits(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewInMemoryKeyModelRuntimeStore(clock)
	now := NowMs(clock)
	// 三个不同 source 的 due 状态，source limit = 2 per sweep 由 Redis 侧承担，
	// memory runner 只按并发与 source 计数选择。
	for index := 0; index < 5; index++ {
		capability := testCapability()
		capability.ClientModel = "model-" + string(rune('a'+index))
		capability.CredentialSourceAccountID = "source-" + string(rune('a'+index%2))
		intent := memoryIntent(capability, "intent-"+string(rune('a'+index)), now)
		intent.RecoveryTarget = &KeyModelRecoveryTarget{AccountID: capability.CredentialSourceAccountID, GroupID: "grp", SystemAccountID: "sys"}
		if _, err := store.RecordFailure(context.Background(), intent); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store:       store,
		Probe:       func(KeyModelRecoveryProbeInput) KeyModelOutcome { return KeyModelOutcomeUnknown },
		Now:         func() int64 { return NowMs(clock) },
		CreateID:    func() string { return "lease" },
		Concurrency: 2,
	})
	clock.Advance(6 * time.Second)
	result := runner.Sweep(context.Background())
	if result.DueCount != 5 {
		t.Fatalf("due = %d, want 5", result.DueCount)
	}
	if result.StartedCount > 2 {
		t.Fatalf("started = %d exceeds concurrency 2", result.StartedCount)
	}
	// 未启动的条目在下一轮仍可被选择。
	second := runner.Sweep(context.Background())
	if second.StartedCount == 0 {
		t.Fatal("remaining due states should start in a later sweep")
	}
}

func TestMemoryRecoveryRunnerUnknownOnProbeError(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewInMemoryKeyModelRuntimeStore(clock)
	capability := testCapability()
	intent := memoryIntent(capability, "intent-1", NowMs(clock))
	intent.RecoveryTarget = &KeyModelRecoveryTarget{AccountID: "source-1", GroupID: "grp", SystemAccountID: "sys"}
	if _, err := store.RecordFailure(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	runner := NewKeyModelMemoryRecoveryRunner(KeyModelMemoryRecoveryRunnerOptions{
		Store: store,
		Probe: func(input KeyModelRecoveryProbeInput) KeyModelOutcome {
			defer func() { recover() }()
			panic("probe exploded")
		},
		Now:      func() int64 { return NowMs(clock) },
		CreateID: func() string { return "lease" },
	})
	clock.Advance(6 * time.Second)
	result := runner.Sweep(context.Background())
	if result.StartedCount != 1 || result.SettledCount != 1 {
		t.Fatalf("panic probe must settle as unknown: %+v", result)
	}
	state, err := store.Get(context.Background(), capability)
	if err != nil || state == nil {
		t.Fatal("state must survive")
	}
}
