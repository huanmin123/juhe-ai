package gatewayresponse

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// D-100 修复验证：检查策略拦截器真实接入流式管线（pipe.go 装配）、codex
// compaction 契约帧形状与端点族门。对齐 Node stream.ts:215-233 的装配条件
// 与 codex-compaction-contract 的失配帧判定。

var interceptorMountPolicy = RuntimeResponseInspectionPolicy{
	ID: "default_openai_context_window_error", Source: PolicySourceSystemDefault, Name: "上下文超限",
	Enabled: true, ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true,
	ScopeType: "provider", ProviderCode: "openai", AccountSwitch: "request_next_account",
	Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"context_length_exceeded"}},
}

var contextWindowErrorChunk = []byte("data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"上下文长度超限\"}}\n\n")

// 策略经 pipe options 传入（不显式注入 Interceptor）：命中即在写下游前拦截。
func TestStreamPipeMountsInterceptorFromPolicies(t *testing.T) {
	recorder := &failureRecorder{}
	result, err := runPipe(NewSliceUpstreamBody(contextWindowErrorChunk), nil, StreamPipeOptions{
		ClientRetryEnabled:                    true,
		RetryBeforeDownstreamWriteUntilOutput: true,
		ResponseInspectionPolicies:            []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		ResponseInspectionContext:             &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("interception must happen before downstream write, bytes = %d", result.DownstreamBytesWritten)
	}
	if result.ResponseInspection == nil || result.ResponseInspection.PolicyID != "default_openai_context_window_error" {
		t.Fatalf("inspection = %+v", result.ResponseInspection)
	}
}

// 策略已装配但事件不命中：字节原样透传到下游，无策略决策。
func TestStreamPipeMountedInterceptorPassesThroughUnmatchedEvents(t *testing.T) {
	recorder := &failureRecorder{}
	result, err := runPipe(NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatDoneChunk)), nil, StreamPipeOptions{
		ClientRetryEnabled:         true,
		ResponseInspectionPolicies: []RuntimeResponseInspectionPolicy{interceptorMountPolicy},
		ResponseInspectionContext:  &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ResponseInspection != nil {
		t.Fatalf("unmatched events must not produce policy decision: %+v", result.ResponseInspection)
	}
	if result.DownstreamBytesWritten == 0 {
		t.Fatalf("passthrough events must reach downstream, result = %+v", result)
	}
}

// 无策略且未开启预提交重试：不装配拦截器，错误事件不走策略决策。
func TestStreamPipeSkipsInterceptorMountWithoutPoliciesOrRetry(t *testing.T) {
	recorder := &failureRecorder{}
	result, err := runPipe(NewSliceUpstreamBody(contextWindowErrorChunk), nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ResponseInspection != nil {
		t.Fatalf("no policies configured, unexpected policy decision: %+v", result.ResponseInspection)
	}
}

// ---- codex compaction 契约帧 ----

var codexCompactionContext = &ResponseInspectionRuntimeContext{
	ClientProfile:              "codex",
	AccountClientCompatibility: "codex_responses",
	CodexCompactionExpected:    true,
}

func newCompactionInterceptor(family gatewayproto.ResponseEndpointFamily) *OpenAIStreamInterceptor {
	policy := RuntimeResponseInspectionPolicy{
		ID: CodexCompactionContractPolicyID, Source: PolicySourceSystemDefault, Enabled: true,
		ExecutionMode: "enforce", DataHandling: "replace_with_failure",
		ScopeType: "provider", ProviderCode: "openai",
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{CodexCompactionContractMismatchErrorCode}},
	}
	return NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		Policies:           []RuntimeResponseInspectionPolicy{policy},
		EndpointFamily:     family,
		Context:            codexCompactionContext,
		CompactionExpected: true,
	})
}

// 契约帧形状：非 compaction 的 output_item.done 产出 raw_json_path 失配帧，
// 事件类型与端点族承载契约语义（对齐 codexCompactionContractMismatchFrame）。
func TestCodexCompactionContractMismatchFrameShape(t *testing.T) {
	frame := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
		OutputItemCount:     1,
		CompactionItemCount: 0,
		Transport:           "sse",
		EventType:           "response.output_item.done",
	})
	if frame == nil {
		t.Fatal("mismatch frame expected")
	}
	if frame.FrameType != gatewayproto.FrameTypeRawJSONPath {
		t.Fatalf("frameType = %v", frame.FrameType)
	}
	if frame.ErrorCode != CodexCompactionContractMismatchErrorCode {
		t.Fatalf("errorCode = %q", frame.ErrorCode)
	}
	if frame.EndpointFamily != gatewayproto.EndpointFamilyResponses || string(frame.Transport) != "sse" {
		t.Fatalf("frame = %+v", frame)
	}
	if frame.EventType != "response.output_item.done" || len(frame.RawJSONPaths) != 1 || frame.RawJSONPaths[0] != "output" {
		t.Fatalf("frame = %+v", frame)
	}
}

// 恰好 1 个 compaction item：不产出失配帧，事件原样透传。
func TestCodexCompactionCompactionItemEventPassesThrough(t *testing.T) {
	interceptor := newCompactionInterceptor(gatewayproto.EndpointFamilyResponses)
	chunk := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"encrypted\"}}\n\n")
	result := interceptor.PushChunk(chunk)
	if result.Intercepted != nil {
		t.Fatalf("exactly-one compaction item must not intercept: %+v", result.Intercepted)
	}
	if len(result.Chunks) != 1 || string(result.Chunks[0]) != string(chunk) {
		t.Fatalf("compaction item event must pass through: %+v", result.Chunks)
	}
}

// 非 compaction 的 output_item.done 在契约上下文内被系统默认策略拦截，
// 决策携带 codex_compaction_contract_mismatch。
func TestCodexCompactionContractFrameInterceptsNonCompactionItem(t *testing.T) {
	interceptor := newCompactionInterceptor(gatewayproto.EndpointFamilyResponses)
	chunk := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":\"hi\"}}\n\n")
	result := interceptor.PushChunk(chunk)
	if result.Intercepted == nil {
		t.Fatalf("contract mismatch must intercept, result = %+v", result)
	}
	if result.Intercepted.UpstreamErrorCode != CodexCompactionContractMismatchErrorCode {
		t.Fatalf("decision = %+v", result.Intercepted)
	}
	if result.Intercepted.PolicyID != CodexCompactionContractPolicyID {
		t.Fatalf("decision = %+v", result.Intercepted)
	}
}

// 契约帧只挂在 responses 端点族：chat 族同上下文不产出契约帧。
func TestCodexCompactionContractFrameGatedByResponsesFamily(t *testing.T) {
	interceptor := newCompactionInterceptor(gatewayproto.EndpointFamilyChatCompletions)
	chunk := []byte("data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"超限\"}}\n\n")
	result := interceptor.PushChunk(chunk)
	if result.Intercepted != nil && result.Intercepted.UpstreamErrorCode == CodexCompactionContractMismatchErrorCode {
		t.Fatalf("compaction contract frame must not attach on chat family: %+v", result.Intercepted)
	}
}
