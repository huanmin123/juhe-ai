package gatewayresponse

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestResponsesRootStatusTracker(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		chunkEvery int
		wantFailed bool
	}{
		{name: "根 status failed", body: `{"status":"failed","error":{"code":"x"}}`, wantFailed: true},
		{name: "根 status completed", body: `{"status":"completed","output":[]}`, wantFailed: false},
		{name: "嵌套 status 不触发", body: `{"output":[{"status":"failed"}],"status":"completed"}`, wantFailed: false},
		{name: "status 位于大 output 之后", body: `{"output":[{"text":"` + strings.Repeat("a", 4096) + `"}],"status":"failed"}`, wantFailed: true},
		{name: "字符串值含 failed 不误报", body: `{"status":"not failed"}`, wantFailed: false},
		{name: "数组根", body: `[{"status":"failed"}]`, wantFailed: false},
		{name: "转义 status 值", body: `{"status":"fa\u0069led"}`, wantFailed: true},
		{name: "分片边界", body: `{"output": [], "status": "failed"}`, chunkEvery: 3, wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewResponsesRootStatusTracker()
			if tt.chunkEvery <= 0 {
				tracker.Push([]byte(tt.body))
			} else {
				for i := 0; i < len(tt.body); i += tt.chunkEvery {
					end := i + tt.chunkEvery
					if end > len(tt.body) {
						end = len(tt.body)
					}
					tracker.Push([]byte(tt.body[i:end]))
				}
			}
			if tracker.HasFailedStatus() != tt.wantFailed {
				t.Fatalf("HasFailedStatus = %v, want %v", tracker.HasFailedStatus(), tt.wantFailed)
			}
		})
	}
}

func TestResponsesFailureStatusFromCapturedJSON(t *testing.T) {
	if !ResponsesFailureStatusFromCapturedJSON(`{"status":"failed"}`) {
		t.Fatal("expected failed")
	}
	if ResponsesFailureStatusFromCapturedJSON("") {
		t.Fatal("empty body is not failed")
	}
}

func TestBuildGatewayStreamReadPlan(t *testing.T) {
	base := TimeoutProfile{FirstResponseTimeoutMs: 10_000, IdleTimeoutMs: 5_000, UncommittedAttemptMaxLifetimeMs: 60_000}
	tests := []struct {
		name            string
		profile         TimeoutProfile
		startedAt       int64
		now             int64
		status          StreamReadPlanStatus
		wantNil         bool
		wantKind        string
		wantDeadline    bool
	}{
		{
			name:     "timeoutsDisabled 无计划",
			profile:  TimeoutProfile{TimeoutsDisabled: true},
			wantNil:  true,
		},
		{
			name:      "首块阶段",
			profile:   base,
			startedAt: 0, now: 1000,
			status:   StreamReadPlanStatus{WaitingForFirstChunk: true},
			wantKind: "first_chunk",
		},
		{
			name:      "活动流 raw idle",
			profile:   base,
			startedAt: 0, now: 1000,
			status:   StreamReadPlanStatus{UpstreamChunkReceived: true, SemanticResultReceived: true, LastUpstreamActivityAt: 1000},
			wantKind: "upstream_activity",
		},
		{
			name:    "语义结果窗口最紧（首响应窗口先于 idle 耗尽）",
			profile: TimeoutProfile{FirstResponseTimeoutMs: 3_000, IdleTimeoutMs: 5_000, UncommittedAttemptMaxLifetimeMs: 60_000},
			startedAt: 0, now: 1000,
			status:   StreamReadPlanStatus{UpstreamChunkReceived: true, LastUpstreamActivityAt: 1000},
			wantKind: "semantic_result",
		},
		{
			name:      "pending event 时 raw idle 优先",
			profile:   base,
			startedAt: 0, now: 1000,
			status:   StreamReadPlanStatus{UpstreamChunkReceived: true, PendingProtocolEvent: true, LastUpstreamActivityAt: 1000},
			wantKind: "upstream_activity",
		},
		{
			name:    "生命周期最紧",
			profile: TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 60_000, UncommittedAttemptMaxLifetimeMs: 5_000},
			status:  StreamReadPlanStatus{WaitingForFirstChunk: true},
			wantKind: "stream_lifetime",
		},
		{
			name:         "deadline 已超",
			profile:      base,
			startedAt:    0, now: 11_000,
			status:       StreamReadPlanStatus{WaitingForFirstChunk: true},
			wantKind:     "first_chunk",
			wantDeadline: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := BuildGatewayStreamReadPlan(tt.profile, tt.startedAt, tt.status, tt.now)
			if tt.wantNil {
				if plan != nil {
					t.Fatalf("plan = %+v, want nil", plan)
				}
				return
			}
			if plan == nil {
				t.Fatal("plan = nil, want non-nil")
			}
			if plan.TimeoutKind != tt.wantKind {
				t.Fatalf("TimeoutKind = %q, want %q", plan.TimeoutKind, tt.wantKind)
			}
			if plan.DeadlineExceeded != tt.wantDeadline {
				t.Fatalf("DeadlineExceeded = %v, want %v", plan.DeadlineExceeded, tt.wantDeadline)
			}
			if !strings.Contains(plan.TimeoutMessage, "s") {
				t.Fatalf("TimeoutMessage %q should carry second granularity", plan.TimeoutMessage)
			}
		})
	}
}

func TestReadPlanMessages(t *testing.T) {
	if FirstChunkTimeoutMessage(5) != "上游流式请求 5s 内未返回首段数据" {
		t.Fatal("first chunk message mismatch")
	}
	if StreamIdleTimeoutMessage(5) != "上游流式响应 5s 内未返回任何新数据" {
		t.Fatal("idle message mismatch")
	}
	if StreamSemanticResultTimeoutMessage(5) != "上游流式响应 5s 内未返回有效输出、失败或终止事件" {
		t.Fatal("semantic message mismatch")
	}
	if StreamMaxLifetimeTimeoutMessage(5) != "上游流式响应已达到最大存活时间 5s，已中断当前连接" {
		t.Fatal("lifetime message mismatch")
	}
}

func TestStreamClientFailureCode(t *testing.T) {
	if StreamClientFailureCode("x", false, true, 0) != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatal("client retry without output should map to retry code")
	}
	if StreamClientFailureCode("x", false, true, 128) != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatal("client retry with downstream bytes should map to retry code")
	}
	if StreamClientFailureCode("x", true, true, 0) != "x" {
		t.Fatal("output received keeps original code")
	}
	if StreamClientFailureCode("x", false, false, 0) != "x" {
		t.Fatal("retry disabled keeps original code")
	}
}

func TestShouldRetryPreCommitStreamFailureOnServer(t *testing.T) {
	response := PreCommitResponseState{}
	if ShouldRetryPreCommitStreamFailureOnServer(StreamPipeResult{ErrorCode: "x"}, response) != true {
		t.Fatal("clean pre-commit failure is replayable")
	}
	if ShouldRetryPreCommitStreamFailureOnServer(StreamPipeResult{ErrorCode: "x", SemanticCommitted: true}, response) {
		t.Fatal("semantic committed cannot retry")
	}
	if ShouldRetryPreCommitStreamFailureOnServer(StreamPipeResult{}, response) {
		t.Fatal("missing error code cannot retry")
	}
	if ShouldRetryPreCommitStreamFailureOnServer(StreamPipeResult{ErrorCode: "x", GatewayLocalFailure: true}, response) {
		t.Fatal("gateway local failure cannot retry")
	}
	if ShouldRetryPreCommitStreamFailureOnServer(StreamPipeResult{ErrorCode: "x"}, PreCommitResponseState{WritableEnded: true}) {
		t.Fatal("ended response cannot retry")
	}
}

func TestPreCommitStreamServerRetryErrorCode(t *testing.T) {
	result := StreamPipeResult{Message: "上游流式响应已中断"}
	if PreCommitStreamServerRetryErrorCode(result, true) != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatal("protocol error strategy should return retry code")
	}
	if PreCommitStreamServerRetryErrorCode(result, false) != gatewaypreauth.GatewayStreamFailureCode(result.Message) {
		t.Fatal("default strategy keeps stable failure code")
	}
}

func TestTransientPrecommitDecision(t *testing.T) {
	decision := &ResponseInspectionDecision{
		PolicySource:     "system_default",
		PolicyID:         "default_openai_transient_precommit_error",
		Reason:           "before_downstream_write_response_failure",
		TriggerPhase:     "before_downstream_write",
	}
	if !IsTransientPrecommitUpstreamFailureDecision(decision) {
		t.Fatal("expected transient precommit decision")
	}
	decision.DownstreamWritten = true
	if IsTransientPrecommitUpstreamFailureDecision(decision) {
		t.Fatal("downstream written disqualifies")
	}
}

func TestShouldExcludeCurrentAccountForStreamServerRetry(t *testing.T) {
	if !ShouldExcludeCurrentAccountForStreamServerRetry(&ResponseInspectionDecision{AccountSwitch: "avoid_account_ttl"}) {
		t.Fatal("avoid_account_ttl excludes")
	}
	if !ShouldExcludeCurrentAccountForStreamServerRetry(&ResponseInspectionDecision{AccountState: "runtime_avoidance"}) {
		t.Fatal("runtime_avoidance excludes")
	}
	if ShouldExcludeCurrentAccountForStreamServerRetry(&ResponseInspectionDecision{}) {
		t.Fatal("empty decision does not exclude")
	}
}

func TestShouldRememberGatewayClientSourceFailure(t *testing.T) {
	if !ShouldRememberGatewayClientSourceFailure(StreamPipeResult{ErrorCode: gatewaypreauth.GatewayStreamClientRetryErrorCode}, true) {
		t.Fatal("retry code failure remembered")
	}
	if ShouldRememberGatewayClientSourceFailure(StreamPipeResult{Completed: true, ErrorCode: gatewaypreauth.GatewayStreamClientRetryErrorCode}, true) {
		t.Fatal("completed stream not remembered")
	}
}

func TestClassifyGatewayUpstreamFailure(t *testing.T) {
	status429 := 429
	status502 := 502
	status400 := 400
	tests := []struct {
		name        string
		input       GatewayUpstreamFailureClassificationInput
		wantClass   GatewayUpstreamFailureClass
		wantReason  GatewayUpstreamFailureMetricReasonClass
	}{
		{"请求阶段", GatewayUpstreamFailureClassificationInput{Phase: "upstream_request"}, FailureClassTransport, MetricReasonTransport},
		{"响应阶段", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response"}, FailureClassOpaqueUpstreamResponse, MetricReasonUnknown},
		{"配额", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", ErrorCode: "insufficient_quota"}, FailureClassOpaqueUpstreamResponse, MetricReasonQuota},
		{"限流", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", StatusCode: &status429}, FailureClassOpaqueUpstreamResponse, MetricReasonRateLimit},
		{"鉴权", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", StatusCode: status401()}, FailureClassOpaqueUpstreamResponse, MetricReasonAuthorization},
		{"5xx", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", StatusCode: &status502}, FailureClassOpaqueUpstreamResponse, MetricReasonUpstream5xx},
		{"4xx", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", StatusCode: &status400}, FailureClassOpaqueUpstreamResponse, MetricReasonUpstream4xx},
		{"超时码", GatewayUpstreamFailureClassificationInput{Phase: "upstream_response", ErrorCode: "ETIMEDOUT"}, FailureClassOpaqueUpstreamResponse, MetricReasonTimeout},
		{"未知阶段", GatewayUpstreamFailureClassificationInput{Phase: "elsewhere"}, FailureClassUnknown, MetricReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := ClassifyGatewayUpstreamFailure(tt.input)
			if obs.FailureClass != tt.wantClass || obs.MetricReasonClass != tt.wantReason {
				t.Fatalf("got %+v", obs)
			}
		})
	}
}

func status401() *int { value := 401; return &value }

func TestClassifyGatewayDispatchExhaustion(t *testing.T) {
	status := 503
	tests := []struct {
		name        string
		lastAttempt *UpstreamAttemptSummary
		want        GatewayDispatchExhaustionReason
	}{
		{"无尝试", nil, DispatchExhaustionNoAvailableAccount},
		{"Key 池不可用", &UpstreamAttemptSummary{UpstreamURL: "account:api_key_pool_unavailable"}, DispatchExhaustionAPIKeyPoolUnavailable},
		{"本地抑制", &UpstreamAttemptSummary{UpstreamURL: "account:locally_suppressed"}, DispatchExhaustionAllAccountsLocallySuppressed},
		{"并发耗尽", &UpstreamAttemptSummary{UpstreamURL: "concurrency:limit"}, DispatchExhaustionAccountConcurrencyExhausted},
		{"HTTP 错误", &UpstreamAttemptSummary{Status: &status}, DispatchExhaustionUpstreamHTTPError},
		{"传输错误", &UpstreamAttemptSummary{UpstreamURL: "https://upstream.example"}, DispatchExhaustionUpstreamTransportError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyGatewayDispatchExhaustion(tt.lastAttempt)
			if got.FailureReason != tt.want {
				t.Fatalf("FailureReason = %q, want %q", got.FailureReason, tt.want)
			}
		})
	}
}

func TestShouldInvalidateProviderModelCatalog(t *testing.T) {
	if !ShouldInvalidateProviderModelCatalog("custom_provider_model_saved") {
		t.Fatal("save invalidates")
	}
	if ShouldInvalidateProviderModelCatalog("unrelated_reason") {
		t.Fatal("unrelated reason ignored")
	}
	if ShouldInvalidateProviderModelCatalog("") {
		t.Fatal("empty reason ignored")
	}
}

func TestDownstreamCommitState(t *testing.T) {
	state := &DownstreamCommitState{}
	if !state.CanRetryUpstream() {
		t.Fatal("fresh state can retry")
	}
	state.MarkTransportCommitted(128)
	if !state.TransportCommitted || state.SemanticCommitted || state.DownstreamBytesWritten != 128 {
		t.Fatalf("transport commit state = %+v", state)
	}
	state.MarkSemanticCommitted(64)
	if state.DownstreamBytesWritten != 192 || !state.SemanticCommitted {
		t.Fatalf("semantic commit state = %+v", state)
	}
	if state.CanRetryUpstream() {
		t.Fatal("semantic committed cannot retry")
	}
	state.MarkSuccessfulProtocolTerminalReceived()
	if !state.SuccessfulProtocolTerminalReceived {
		t.Fatal("terminal flag missing")
	}
	state.MarkTransportCommitted(-5)
	if state.DownstreamBytesWritten != 192 {
		t.Fatal("negative bytes normalized")
	}
}

func TestLimitedCaptureDiagnostics(t *testing.T) {
	capture := NewLimitedCapture(8)
	capture.Push([]byte("0123456789"))
	if !capture.IsTruncated() {
		t.Fatal("expected truncation")
	}
	if string(capture.Buffer()) != "01234567" {
		t.Fatalf("buffer = %q", capture.Buffer())
	}
	if capture.CompleteBuffer() != nil {
		t.Fatal("truncated capture has no complete buffer")
	}
	text, ok := capture.ToDiagnosticText()
	if !ok || text != "01234567\n[truncated]" {
		t.Fatalf("diagnostic = %q ok=%v", text, ok)
	}
	capture.Clear()
	if capture.IsTruncated() || len(capture.Buffer()) != 0 {
		t.Fatal("clear resets")
	}
	empty := NewLimitedCapture(-1)
	empty.Push([]byte("data"))
	if empty.Buffer() != nil && len(empty.Buffer()) != 0 {
		t.Fatal("negative limit disables capture")
	}
}
