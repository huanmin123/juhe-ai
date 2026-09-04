package gatewayresponse

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---- 测试 mock：审计捕获 / usage 记录 / 账号副作用 / 完成观察 ----

type mockAuditCapture struct {
	mu               sync.Mutex
	metadata         []map[string]any
	completed        []AttemptAuditInput
	finalized        []gatewaypreauth.AuditFinalizeInput
	omitted          []OmitPayloadBodiesInput
	captureSuccesses bool
}

func newMockAuditCapture() *mockAuditCapture {
	return &mockAuditCapture{captureSuccesses: true}
}

func (m *mockAuditCapture) BindContext(gatewaypreauth.AuditGatewayContext)       {}
func (m *mockAuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	merged := map[string]any{"__label": label}
	for key, value := range metadata {
		merged[key] = value
	}
	m.metadata = append(m.metadata, merged)
}

func (m *mockAuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalized = append(m.finalized, input)
}

func (m *mockAuditCapture) CompleteAttempt(_ string, input AttemptAuditInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, input)
}

func (m *mockAuditCapture) FinalizeLazy(provider func() gatewaypreauth.AuditFinalizeInput) {
	m.Finalize(provider())
}

func (m *mockAuditCapture) OmitPayloadBodies(input OmitPayloadBodiesInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.omitted = append(m.omitted, input)
}

func (m *mockAuditCapture) ShouldCaptureSuccessPayloads() bool { return m.captureSuccesses }

type mockUsageRecords struct {
	mu        sync.Mutex
	completed []CompletedAttemptInput
	failed    []FailedAttemptInput
	failures  []FailureUsageRecordInput
	dispatch  []ModelsUsageDispatchInput
}

func (m *mockUsageRecords) RecordCompletedUpstreamAttempt(input CompletedAttemptInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, input)
}

func (m *mockUsageRecords) RecordFailedUpstreamAttempt(input FailedAttemptInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = append(m.failed, input)
}

func (m *mockUsageRecords) RecordGatewayFailure(input FailureUsageRecordInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, input)
}

func (m *mockUsageRecords) DispatchUsageRecord(input ModelsUsageDispatchInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatch = append(m.dispatch, input)
}

func (m *mockUsageRecords) failureCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.failures)
}

func (m *mockUsageRecords) lastFailure() FailureUsageRecordInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failures[len(m.failures)-1]
}

func (m *mockUsageRecords) dispatchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.dispatch)
}

type mockAccountEffects struct {
	mu           sync.Mutex
	streamFail   int
	affinity     []string
	healthChecks []string
	sideEffects  []string
}

func (m *mockAccountEffects) HandleStreamFailure(account AccountView, message string, errorCode string, context StreamFailureContext, shouldMutateAccount bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamFail++
	return nil
}

func (m *mockAccountEffects) ForgetSessionAffinity(sessionAffinityKey string, accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.affinity = append(m.affinity, sessionAffinityKey+":"+accountID)
}

func (m *mockAccountEffects) DispatchRequestFailureAccountHealthCheck(trafficSource string, accountID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthChecks = append(m.healthChecks, accountID)
	return true
}

func (m *mockAccountEffects) ApplyInspectionPolicySideEffects(decision *ResponseInspectionDecision, account AccountView, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sideEffects = append(m.sideEffects, decision.PolicyID)
	return nil
}

type mockCompletion struct {
	ch chan int64
}

func (m *mockCompletion) Wait() <-chan int64 { return m.ch }

type mockHTTPObserver struct {
	ch chan int64
}

func (m *mockHTTPObserver) Observe(res gatewaypreauth.GatewayResponseWriter) HTTPCompletion {
	return &mockCompletion{ch: m.ch}
}

type mockCatalogLoader struct {
	items []ModelCatalogEntry
}

func (m *mockCatalogLoader) ListClientModelCatalog(systemAccountID string, providerCodes []string) []ModelCatalogEntry {
	if len(providerCodes) > 0 && providerCodes[0] == "anthropic" {
		return []ModelCatalogEntry{{Model: "claude-x", Scope: "built_in", ReleaseDate: "2025-01-02"}}
	}
	if len(providerCodes) > 0 && providerCodes[0] == "gemini" {
		return []ModelCatalogEntry{{Model: "gemini-x", Scope: "built_in", CapabilityNotes: "fast"}}
	}
	return []ModelCatalogEntry{
		{Model: "gpt-x", Scope: "built_in", ReleaseDate: "2024-06-01"},
		{Model: "custom-y", Scope: "personal", CreatedAt: "2025-03-04T05:06:07Z", CapabilityNotes: "自定义模型"},
	}
}

var _ = http.Header{}
var _ = time.Now

func staticSignal() interface {
	Done() <-chan struct{}
	Err() error
} {
	return staticSignalValue
}

type staticSignalType struct{}

func (staticSignalType) Done() <-chan struct{} { return nil }
func (staticSignalType) Err() error            { return nil }

var staticSignalValue staticSignalType

func usageContextFixture() gatewaypreauth.GatewayFailureUsageContext {
	return gatewaypreauth.GatewayFailureUsageContext{
		TraceID:         "trace-1",
		TrafficSource:   "gateway",
		SystemAccountID: "sys-1",
		APIKeyID:        "key-1",
		GroupID:         "group-1",
		Endpoint:        "/v1/chat/completions",
		RequestSnapshot: gatewaypreauth.UsageRequestSnapshot{
			Method: "POST",
			Path:   "/v1/chat/completions",
		},
	}
}

func accountFixture() AccountView {
	return OpenAIAccountView{Account: gatewayAccountFixture}
}

func TestUsageFallbackHookShape(t *testing.T) {
	hook := func(driver ResponseDriverPort, usage gatewayproto.ParsedUsage, input UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int) {
		if !input.OutputReceived {
			return usage, false, nil, nil
		}
		estimated := 11
		usage.OutputTokens = &estimated
		return usage, true, nil, &estimated
	}
	updated, estimated, _, out := hook(nil, gatewayproto.EmptyUsage(), UsageFallbackInput{OutputReceived: true, EstimatedOutputTokens: 11})
	if !estimated || out == nil || *out != 11 || updated.OutputTokens == nil {
		t.Fatalf("fallback = %+v", updated)
	}
	_ = gatewayproto.EmptyUsage()
	_ = gatewayproto.ErrorPayload{}
}
