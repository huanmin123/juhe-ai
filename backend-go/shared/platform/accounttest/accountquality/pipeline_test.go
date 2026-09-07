package accountquality

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mock 基础设施（可回放、结果稳定）

type fakeLogger struct {
	mu     sync.Mutex
	events []logEvent
}

type logEvent struct {
	level   string
	event   string
	fields  map[string]any
	message string
}

func (l *fakeLogger) log(level, event string, fields map[string]any, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logEvent{level: level, event: event, fields: fields, message: message})
}
func (l *fakeLogger) Debug(event string, fields map[string]any, message string) {
	l.log("debug", event, fields, message)
}
func (l *fakeLogger) Info(event string, fields map[string]any, message string) {
	l.log("info", event, fields, message)
}
func (l *fakeLogger) Warn(event string, fields map[string]any, message string) {
	l.log("warn", event, fields, message)
}
func (l *fakeLogger) Error(event string, fields map[string]any, message string) {
	l.log("error", event, fields, message)
}
func (l *fakeLogger) findByEvent(event string) *logEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.events {
		if l.events[i].event == event {
			return &l.events[i]
		}
	}
	return nil
}

type mockReader struct {
	accounts map[string]*AccountForTest
	group    map[string]*OpenAIAccountCandidate
	keys     map[string]bool // "candidateID|fingerprint|apiKey"
}

func (m *mockReader) FindAccountForTest(ctx context.Context, accountID string) (*AccountForTest, error) {
	return m.accounts[accountID], nil
}
func (m *mockReader) FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*OpenAIAccountCandidate, error) {
	return m.group[accountID], nil
}
func (m *mockReader) HasAPIKeyEntry(ctx context.Context, candidate *OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error) {
	return m.keys[candidate.ID+"|"+fingerprint+"|"+apiKey], nil
}

type mockProber struct {
	mu          sync.Mutex
	observation *ProbeObservation
	err         error
	calls       int
	lastRequest *ProbeRequest
}

func (m *mockProber) Probe(ctx context.Context, req ProbeRequest) (*ProbeObservation, error) {
	m.mu.Lock()
	m.calls++
	m.lastRequest = &req
	obs, err := m.observation, m.err
	m.mu.Unlock()
	return obs, err
}

type mockPrecheckMutation struct {
	mu     sync.Mutex
	result PrecheckMutationResult
	inputs []PrecheckMutationInput
}

func (m *mockPrecheckMutation) snapshot() []PrecheckMutationInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PrecheckMutationInput, len(m.inputs))
	copy(out, m.inputs)
	return out
}

func (m *mockPrecheckMutation) MarkPrecheckTemporaryUnavailable(ctx context.Context, input PrecheckMutationInput) (PrecheckMutationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, input)
	return m.result, nil
}

type mockCooldownCandidates struct {
	candidates []CooldownProbeCandidate
}

func (m *mockCooldownCandidates) ListDueForProbe(ctx context.Context, limit int) ([]CooldownProbeCandidate, error) {
	if limit < len(m.candidates) {
		return m.candidates[:limit], nil
	}
	return m.candidates, nil
}

type mockCooldownMutation struct {
	mu        sync.Mutex
	success   KeyMutationResult
	failures  int
	deferred  int
	lastDefer *KeyDeferInput
	lastFail  *KeyFailureInput
}

func (m *mockCooldownMutation) RecordKeySuccess(ctx context.Context, input KeySuccessInput) (KeyMutationResult, error) {
	return m.success, nil
}
func (m *mockCooldownMutation) RecordKeyFailure(ctx context.Context, input KeyFailureInput) (KeyMutationResult, error) {
	m.mu.Lock()
	m.failures++
	m.lastFail = &input
	m.mu.Unlock()
	return KeyMutationResult{Changed: true}, nil
}
func (m *mockCooldownMutation) DeferKeyProbe(ctx context.Context, input KeyDeferInput) (KeyMutationResult, error) {
	m.mu.Lock()
	m.deferred++
	m.lastDefer = &input
	m.mu.Unlock()
	return KeyMutationResult{Changed: true}, nil
}

func (m *mockCooldownMutation) failSnapshot() *KeyFailureInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastFail == nil {
		return nil
	}
	copied := *m.lastFail
	return &copied
}

func (m *mockCooldownMutation) deferSnapshot() *KeyDeferInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastDefer == nil {
		return nil
	}
	copied := *m.lastDefer
	return &copied
}

func baseAccount(id string) *AccountForTest {
	return &AccountForTest{
		ID: id, Name: "账户-" + id, Type: "api_key", Status: AccountStatusActive, Schedulable: true,
		BoundGroupID: "group-1", SystemAccountID: "sys-1",
	}
}

func successObservation() *ProbeObservation {
	status := 200
	return &ProbeObservation{
		Result:   ProbeResult{Success: true, StatusCode: &status, DurationMs: 42},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 200},
	}
}

func precheckCandidate(accountID string) FailurePrecheckCandidate {
	rate := 0.25
	return FailurePrecheckCandidate{
		AccountID: accountID, SystemAccountID: "sys-1", ProviderCode: "openai",
		RecentRequestCount: 10, RecentSuccessCount: 2, RecentErrorCount: 8, SuccessRate: &rate,
		LastErrorAt: "2026-09-04T07:58:00.000Z", LastErrorMessage: "上游 502",
		UpdatedAt: "2026-09-04T08:00:00.000Z",
	}
}

func dispatchCandidate(accountID string) *OpenAIAccountCandidate {
	return &OpenAIAccountCandidate{ID: accountID, Name: "账户-" + accountID, Type: "api_key", Status: AccountStatusActive, DispatchRevision: 7, HasDispatchRevision: true}
}

// drainWait drains a queue with timeout.
func drainWait(t *testing.T, runner *PrecheckRunner) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runner.StopAndDrain(2 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("队列未在超时前排空")
	}
}

// ---------------------------------------------------------------------------
// precheck

func TestPrecheckRecoversOnSuccess(t *testing.T) {
	logger := &fakeLogger{}
	prober := &mockProber{observation: successObservation()}
	reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")}, group: map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")}}
	mutation := &mockPrecheckMutation{result: PrecheckMutationResult{Updated: true}}
	runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: mutation, Concurrency: 1})
	cleanupRunner(t, runner)

	if !runner.Enqueue(precheckCandidate("acc-1")) {
		t.Fatal("首队应入队成功")
	}
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_recovered") != nil })
	if prober.calls != 1 {
		t.Fatalf("应调用一次探针: %d", prober.calls)
	}
	if len(mutation.inputs) != 0 {
		t.Fatal("成功不应写状态")
	}
}

func TestPrecheckMarksTemporaryUnavailableWithExactReason(t *testing.T) {
	logger := &fakeLogger{}
	status := 502
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, StatusCode: &status, ErrorCode: "upstream_502", Message: "Bad Gateway", DurationMs: 88},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 502},
	}}
	reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")}, group: map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")}}
	mutation := &mockPrecheckMutation{result: PrecheckMutationResult{Updated: true, SkippedReason: ""}}
	runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: mutation, Concurrency: 1})
	cleanupRunner(t, runner)

	runner.Enqueue(precheckCandidate("acc-1"))
	waitFor(t, func() bool { return len(mutation.snapshot()) == 1 })
	input := mutation.snapshot()[0]
	wantReason := "近期质量频繁失败，后台确认失败后标记为临时不可调用；近窗口 10 次请求失败 8 次；成功率 25%；最后业务失败 2026-09-04T07:58:00.000Z；确认 HTTP 502；upstream_502；Bad Gateway"
	if input.Reason != wantReason {
		t.Fatalf("reason 逐字节不符:\n got %q\nwant %q", input.Reason, wantReason)
	}
	if input.ExpectedDispatchRevision != 7 || input.ExpectedStatus != AccountStatusActive || input.PrecheckStartedAt == "" {
		t.Fatalf("fence 字段不符: %+v", input)
	}
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_marked") != nil })
}

func TestPrecheckIneligiblePaths(t *testing.T) {
	cases := []struct {
		name        string
		account     *AccountForTest
		group       *OpenAIAccountCandidate
		observation *ProbeObservation
		wantEvent   string
	}{
		{"账户缺失", nil, nil, nil, "background_account_quality_failure_precheck_discarded"},
		{"调度代次缺失", baseAccount("acc-1"), &OpenAIAccountCandidate{ID: "acc-1", Status: AccountStatusActive}, nil, "background_account_quality_failure_precheck_discarded"},
		{"探针任务失败不写状态", baseAccount("acc-1"), dispatchCandidate("acc-1"),
			&ProbeObservation{Result: ProbeResult{}, Evidence: ProbeEvidence{}}, "background_account_quality_failure_precheck_ineligible_failure_discarded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := &fakeLogger{}
			reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": tc.account}, group: map[string]*OpenAIAccountCandidate{"acc-1": tc.group}}
			mutation := &mockPrecheckMutation{}
			prober := &mockProber{observation: tc.observation}
			runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: mutation, Concurrency: 1})
			cleanupRunner(t, runner)
			runner.Enqueue(precheckCandidate("acc-1"))
			waitFor(t, func() bool { return logger.findByEvent(tc.wantEvent) != nil })
			if len(mutation.snapshot()) != 0 {
				t.Fatal("不应写状态")
			}
		})
	}
}

func TestPrecheckExhaustedOnProbeError(t *testing.T) {
	logger := &fakeLogger{}
	prober := &mockProber{err: errors.New("dial tcp: i/o timeout")}
	reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")}, group: map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")}}
	runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	cleanupRunner(t, runner)
	runner.Enqueue(precheckCandidate("acc-1"))
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_exhausted") != nil })
	if logger.findByEvent("background_account_quality_failure_precheck_exhausted").message != "账户质量失败确认任务已用尽，本轮跳过" {
		t.Fatalf("文案不符: %q", logger.findByEvent("background_account_quality_failure_precheck_exhausted").message)
	}
}

func TestPrecheckRecentDedup(t *testing.T) {
	logger := &fakeLogger{}
	runner := NewPrecheckRunner(PrecheckDeps{
		Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1,
	})
	cleanupRunner(t, runner)
	// 首次入队并完成 discard（记住已确认）。
	reader := &mockReader{accounts: map[string]*AccountForTest{}} // 账户缺失 → discard + remember
	runner.reader = reader
	runner.Enqueue(precheckCandidate("acc-1"))
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_discarded") != nil })
	if runner.Enqueue(precheckCandidate("acc-1")) {
		t.Fatal("30 分钟内重复入队应被拒绝")
	}
}

// ---------------------------------------------------------------------------
// cooldown-retest

func cooldownCandidate(accountID, fingerprint, apiKey string) CooldownProbeCandidate {
	return CooldownProbeCandidate{
		AccountID: accountID, AccountName: "账户-" + accountID, KeyFingerprint: fingerprint, KeyIndex: 0,
		APIKey: apiKey, Status: AccountStatusRateLimited,
		NextProbeAt: "2026-09-04T07:00:00.000Z", StateUpdatedAt: "2026-09-04T07:00:00.000Z",
		AccountConfigRevision: 3, ProbeClaimToken: "token-1", ProbeClaimedUntil: "2026-09-04T07:05:00.000Z",
	}
}

func cooldownReader() *mockReader {
	return &mockReader{
		accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
		group:    map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")},
		keys:     map[string]bool{"acc-1|fp-1|sk-key": true},
	}
}

func TestCooldownRestoresOnSuccess(t *testing.T) {
	logger := &fakeLogger{}
	prober := &mockProber{observation: successObservation()}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_restored") != nil })
	if mutation.success.Changed {
		// RecordKeySuccess mock 默认 zero 值，这里只验证事件。
		_ = mutation
	}
	if prober.lastRequest.TrafficSource != "cooldown_retest" || prober.lastRequest.FixedKeyFingerprint != "fp-1" {
		t.Fatalf("探针请求应钉住 Key: %+v", prober.lastRequest)
	}
}

func TestCooldownQuotaExplicitReset(t *testing.T) {
	logger := &fakeLogger{}
	status := 402
	body := `{"error":{"code":"insufficient_quota","message":"余额不足","reset_at":1798000000}}`
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, StatusCode: &status, ResponseBodyText: body},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
	}}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return mutation.failSnapshot() != nil })
	last := mutation.failSnapshot()
	if last.Status != AccountStatusRateLimited || last.ErrorCode != QuotaRecoveryExplicitErrorCode {
		t.Fatalf("explicit_reset 状态/错误码不符: %+v", last)
	}
	if last.CooldownUntil == "" {
		t.Fatal("explicit_reset 应带上游冷却时间")
	}
	event := logger.findByEvent("background_account_api_key_quota_retest_failed")
	if event == nil || event.message != "API Key 额度仍不足，已严格按上游恢复时间等待下次复测" {
		t.Fatalf("文案不符: %+v", event)
	}
}

func TestCooldownQuotaRecoveryTimeout(t *testing.T) {
	logger := &fakeLogger{}
	status := 402
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, StatusCode: &status, Message: "insufficient quota"},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
	}}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	candidate := cooldownCandidate("acc-1", "fp-1", "sk-key")
	// 上次错误码为 generic 且恢复已开始超过 30 天 → 判定超时。
	candidate.LastErrorCode = QuotaRecoveryGenericErrorCode
	candidate.RecoveryStartedAt = "2026-07-01T00:00:00.000Z"
	runner.Enqueue(candidate, 24)
	waitFor(t, func() bool { return mutation.failSnapshot() != nil })
	last := mutation.failSnapshot()
	if last.Status != AccountStatusError || last.ErrorCode != QuotaRecoveryTimeoutErrorCode || last.CooldownUntil != "" {
		t.Fatalf("30 天超时收口不符: %+v", last)
	}
	event := logger.findByEvent("background_account_api_key_quota_recovery_timeout")
	if event == nil || event.message != "API Key 额度连续确认失败已达到 30 天，进入人工恢复的异常状态" {
		t.Fatalf("文案不符: %+v", event)
	}
}

func TestCooldownTransportFailureRecords(t *testing.T) {
	logger := &fakeLogger{}
	// 传输不完整（连接失败，无完整 framing）→ upstream_failure → 记失败。
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, ErrorCode: "upstream_500", Message: "Internal Server Error", TraceID: "trace-1"},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureConnection},
	}}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return mutation.failSnapshot() != nil })
	last := mutation.failSnapshot()
	// 无前次额度错误码 → breakQuotaRecoveryWindow=false（与 Node 一致）。
	if last.Status != AccountStatusTemporaryUnavail || last.ErrorCode != "upstream_500" || last.BreakQuotaRecoveryWindow {
		t.Fatalf("upstream_failure 记录不符: %+v", last)
	}
	if last.ProbeOutcome != "upstream_failure" {
		t.Fatalf("probeOutcome 不符: %s", last.ProbeOutcome)
	}
}

func TestCooldownNeutralDefers(t *testing.T) {
	logger := &fakeLogger{}
	// 探针任务失败（无上游尝试）→ probe_task_failure → defer 60s。
	prober := &mockProber{observation: &ProbeObservation{Result: ProbeResult{}, Evidence: ProbeEvidence{}}}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return mutation.deferSnapshot() != nil })
	if mutation.deferSnapshot().DelaySeconds != CooldownDefaultDeferSeconds {
		t.Fatalf("中性结果默认顺延 60s: %d", mutation.deferSnapshot().DelaySeconds)
	}
	if logger.findByEvent("background_account_api_key_cooldown_retest_task_failed") == nil {
		t.Fatal("应有 task_failed 事件")
	}
}

func TestCooldownDiscardPaths(t *testing.T) {
	t.Run("凭据已轮换", func(t *testing.T) {
		logger := &fakeLogger{}
		reader := cooldownReader()
		reader.keys = map[string]bool{} // Key 不存在
		prober := &mockProber{}
		runner := NewCooldownRetestRunner(CooldownDeps{
			Logger: logger, Reader: reader, Prober: prober, Mutation: &mockCooldownMutation{},
			Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
		})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool {
			return logger.findByEvent("background_account_api_key_cooldown_retest_stale_credential_discarded") != nil
		})
		if prober.calls != 0 {
			t.Fatal("凭据轮换不应触发探针")
		}
	})
	t.Run("账户已失效", func(t *testing.T) {
		logger := &fakeLogger{}
		reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": {ID: "acc-1", Type: "api_key", Status: AccountStatusError, Schedulable: false}}}
		runner := NewCooldownRetestRunner(CooldownDeps{
			Logger: logger, Reader: reader, Prober: &mockProber{}, Mutation: &mockCooldownMutation{},
			Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
		})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_discarded") != nil })
	})
}

func TestCooldownScanRespectsSlots(t *testing.T) {
	logger := &fakeLogger{}
	mutation := &mockCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: &mockProber{observation: successObservation()},
		Mutation: mutation,
		Candidates: &mockCooldownCandidates{candidates: []CooldownProbeCandidate{
			cooldownCandidate("acc-1", "fp-1", "sk-key"),
			cooldownCandidate("acc-1", "fp-2", "sk-key2"),
			cooldownCandidate("acc-1", "fp-3", "sk-key3"),
		}},
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	if err := runner.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_completed") != nil })
	event := logger.findByEvent("background_account_api_key_cooldown_retest_completed")
	// 队列空闲 → 槽位 2，limit=min(batch=10, slots=2)=2。
	if event.fields["candidateCount"] != 2 {
		t.Fatalf("候选应按剩余槽位圈定为 2: %v", event.fields)
	}
}

// ---------------------------------------------------------------------------
// refresh 编排

func TestRefreshRunnerOrchestration(t *testing.T) {
	store, clock, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	minute := MinuteKey(clock.Now().Add(-time.Minute), time.UTC)
	store.seedMinute(t, "acc-1", minute, 10, 2, 8, 0, 0, "upstream 500")
	if err := store.MarkQualityDirty(ctx, "acc-1"); err != nil {
		t.Fatal(err)
	}

	logger := &fakeLogger{}
	caches := &mockCacheInvalidator{}
	prober := &mockProber{observation: successObservation()}
	reader := &mockReader{accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")}, group: map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")}}
	mutation := &mockPrecheckMutation{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: mutation, Concurrency: 1})
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: caches, Precheck: precheck,
		IngestGate: allowIngestGate{},
		Settings: func(key string, min, max int) int {
			if key == "accountQualityWindowMinutes" {
				return 10
			}
			return max
		},
		Concurrency: func() int { return 4 },
	})
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// 候选 1 条 → 入队并探针成功。
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_refresh_completed") != nil })
	waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_recovered") != nil })
	if !caches.called {
		t.Fatal("有刷新/候选时应清网关运行时缓存")
	}
	event := logger.findByEvent("background_account_quality_refresh_completed")
	if event.fields["failurePrecheckEnqueuedCount"] != 1 {
		t.Fatalf("入队数不符: %v", event.fields)
	}
	drainWait(t, precheck)
}

func TestRefreshRunnerEmptyRun(t *testing.T) {
	store, _, _ := newQualityStore(t)
	logger := &fakeLogger{}
	caches := &mockCacheInvalidator{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: caches, Precheck: precheck,
		IngestGate: allowIngestGate{},
		Settings:   func(string, int, int) int { return 10 }, Concurrency: func() int { return 2 },
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if caches.called {
		t.Fatal("空转不应清缓存")
	}
	if logger.findByEvent("background_account_quality_refresh_completed") != nil {
		t.Fatal("空转不应打完成事件")
	}
}

// allowIngestGate 恒放行的排干门控桩。
type allowIngestGate struct{}

func (allowIngestGate) EnsureUsageRecordsIngested(context.Context) error { return nil }

// denyIngestGate 恒拒绝的排干门控桩（模拟 Node 队列未排干跳过本轮）。
type denyIngestGate struct{ called *bool }

func (d denyIngestGate) EnsureUsageRecordsIngested(context.Context) error {
	if d.called != nil {
		*d.called = true
	}
	return errIngestNotDrained
}

var errIngestNotDrained = errors.New("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")

func TestRefreshRunnerIngestGate(t *testing.T) {
	store, _, _ := newQualityStore(t)
	logger := &fakeLogger{}
	caches := &mockCacheInvalidator{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	// 门控失败 → 本轮直接失败，不推进统计、不打完成事件（Node
	// ensureUsageRecordsIngestedBeforeStatsAggregation 抛错语义）。
	denied := false
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: caches, Precheck: precheck,
		IngestGate: denyIngestGate{called: &denied},
		Settings:   func(string, int, int) int { return 10 }, Concurrency: func() int { return 2 },
	})
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("队列未排干时本轮必须失败")
	}
	if !denied {
		t.Fatal("门控应在本轮被调用")
	}
	if logger.findByEvent("background_account_quality_refresh_completed") != nil {
		t.Fatal("门控失败不应打完成事件")
	}
	if caches.called {
		t.Fatal("门控失败不应清缓存")
	}
	// nil 门控 = Node 必填依赖缺失 → 任务失败，不静默降级。
	bare := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: caches, Precheck: precheck,
		Settings: func(string, int, int) int { return 10 },
	})
	if err := bare.Run(context.Background()); err == nil {
		t.Fatal("nil 门控必须显式失败（Node 必填依赖）")
	}
}

type mockCacheInvalidator struct{ called bool }

func (m *mockCacheInvalidator) ClearGatewayRuntimeCache(ctx context.Context) { m.called = true }

// ---------------------------------------------------------------------------
// queue 行为

func TestRetryQueueDedupAndExhaustion(t *testing.T) {
	logger := &fakeLogger{}
	runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	// 阻塞 run：用账户缺失路径也会立即结束，这里直接测 Enqueue 去重。
	if !runner.Enqueue(precheckCandidate("acc-9")) {
		t.Fatal("首队应成功")
	}
	if runner.Enqueue(precheckCandidate("acc-9")) {
		t.Fatal("同名 key 应去重")
	}
	// 队列零重试：discard 后 key 被删除。
	waitFor(t, func() bool { return !runner.queue.HasKey("acc-9") })
}

func TestRetryQueueSnapshot(t *testing.T) {
	logger := &fakeLogger{}
	blocker := make(chan struct{})
	release := make(chan struct{})
	runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &blockingReader{blocker: blocker, release: release}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	_ = runner.Enqueue(precheckCandidate("acc-1"))
	waitFor(t, func() bool { return runner.Snapshot().RunningCount == 1 })
	snap := runner.Snapshot()
	if snap.RunningCount != 1 || snap.PendingCount != 0 {
		t.Fatalf("快照不符: %+v", snap)
	}
	close(release)
	waitFor(t, func() bool { return runner.Snapshot().RunningCount == 0 })
}

type blockingReader struct {
	blocker chan struct{}
	release chan struct{}
}

func (b *blockingReader) FindAccountForTest(ctx context.Context, accountID string) (*AccountForTest, error) {
	<-b.release
	return nil, nil
}
func (b *blockingReader) FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*OpenAIAccountCandidate, error) {
	return nil, nil
}
func (b *blockingReader) HasAPIKeyEntry(ctx context.Context, candidate *OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error) {
	return false, nil
}

// cleanupRunner 在测试结束时排空队列（不阻断用例执行）。
func cleanupRunner(t *testing.T, runner *PrecheckRunner) {
	t.Helper()
	t.Cleanup(func() { runner.StopAndDrain(2 * time.Second) })
}

// waitFor 轮询等待条件成立（mock 闭环的可回放等待）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
